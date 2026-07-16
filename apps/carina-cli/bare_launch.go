package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tui/theme"

	tea "charm.land/bubbletea/v2"
)

// Capture the package defaults at init so temporary rebinds of
// localdaemon.Dial/Spawn (for tests) cannot recurse through these hooks.
var (
	defaultLocalDial  = localdaemon.Dial
	defaultLocalSpawn = localdaemon.Spawn
)

// spawnDaemonHook lets tests observe/replace the actual daemon spawn without
// starting a real process (mirrors dialHook's seam in client.go).
var spawnDaemonHook = func() error {
	socket, err := defaultSocketPath()
	if err != nil {
		return err
	}
	return defaultLocalSpawn(socket)
}

// dialSocketHook lets tests observe/replace ensureDaemonReachable's dial
// calls without touching a real unix socket (mirrors dialHook's seam in
// client.go). Unlike dialHook, this takes an explicit socket path — the
// same shape as rpc.Dial and ensureDaemonReachable itself — since
// ensureDaemonReachable is handed its socket path by its caller rather than
// resolving it internally.
var dialSocketHook = defaultLocalDial

// daemonReachableDeadline bounds how long ensureDaemonReachable retries the
// dial after auto-starting the daemon before giving up. A package-level var
// (rather than a hardcoded literal in ensureDaemonReachable) so tests can
// shrink it and exercise the give-up path in well under 10 real seconds.
var daemonReachableDeadline = 10 * time.Second

// runBareTUI is bare `carina`'s P1.5(a) entry point: auto-start the daemon
// if it is unreachable, resume the most-recent session for cwd (or fall
// through to a fresh one, exactly as `carina run` does today), then launch
// go/tui in-process — no exec/fork, apps/carina-cli imports go/tui directly
// exactly as apps/carina-tui/main.go does.
func runBareTUI() tui.Outcome {
	bootstrapLocale := microcopy.DetectBootstrapLocale()
	socket, err := defaultSocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapResolveHomeFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	cfg, err := config.Load(home, cwd)
	if err != nil {
		if errors.Is(err, config.ErrInvalidTUILocale) {
			fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage
		}
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	loc, err := resolveBareTUILocale(cfg.TUILocale)
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeUsage
	}
	keybindings, err := tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage
	}

	call, err := ensureDaemonReachable(socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeDaemonUnreachable
	}

	maybeAutoRunDoctor(call)

	sessionID, err := tui.LatestPendingSubmissionSession(cfg.StateDir, cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapRecoveryFailed, microcopy.Args{"reason": err.Error()}, loc))
		call.Close()
		return tui.OutcomeRuntimeError
	}
	if sessionID == "" {
		sessionID = resumeMostRecentOrFresh(call, cwd)
	}
	call.Close()

	th := theme.New(theme.Detect(os.Getenv, true))
	model, err := tui.NewChecked(tui.Options{
		Theme:             th,
		Locale:            loc,
		Socket:            socket,
		SessionID:         sessionID,
		WorkspaceRoot:     cwd,
		StateDir:          cfg.StateDir,
		Keybindings:       keybindings,
		NoAlternateScreen: strings.EqualFold(cfg.TUIAlternateScreen, "never"),
		KeymapUpdater:     bareTUIKeymapUpdater(home, cwd),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage
	}
	defer model.Close()
	prog := tea.NewProgram(model)
	watchCtx, stopWatching := context.WithCancel(context.Background())
	defer stopWatching()
	go tui.WatchKeybindings(watchCtx, home, cwd, prog)
	tui.Connect(prog, socket, sessionID, cwd)

	final, err := prog.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, microcopy.Bootstrap(microcopy.BootstrapRuntimeFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeRuntimeError
	}
	if m, ok := final.(*tui.Model); ok {
		return m.Outcome()
	}
	return tui.OutcomeOK
}

func resolveBareTUILocale(configLocale string) (string, error) {
	return microcopy.ResolveLocale("", configLocale)
}

func bareTUIKeymapUpdater(home, projectRoot string) tui.KeymapUpdateFunc {
	path := filepath.Join(projectRoot, ".carina", "config.json")
	return func(action string, keys []string, remove bool) ([]tui.KeyBindingOverride, error) {
		_, locks, err := config.LoadWithManaged(home, projectRoot, config.DefaultManagedPath())
		if err != nil {
			return nil, err
		}
		if locks.Locked("tui_keybindings") {
			return nil, fmt.Errorf("tui_keybindings is locked by %s", locks.Source)
		}
		if err := config.UpdateTUIKeybinding(path, action, keys, remove); err != nil {
			return nil, err
		}
		cfg, err := config.Load(home, projectRoot)
		if err != nil {
			return nil, err
		}
		return tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	}
}

// resumeMostRecentOrFresh resolves the session bare `carina` should attach
// to: the most recent session for cwd if one exists (per
// latestSessionForWorkspace), otherwise "" so tui.Connect creates a fresh
// one — exactly the "resume-most-recent-or-fresh" contract, reusing
// session.list rather than a separate resume RPC. Failure to list sessions
// degrades to "" (fresh session) rather than blocking the TUI launch.
func resumeMostRecentOrFresh(call Caller, cwd string) string {
	var sessions []sessionSummary
	if err := call.Call("session.list", map[string]any{}, &sessions); err != nil {
		return ""
	}
	id, ok := latestSessionForWorkspace(sessions, cwd)
	if !ok {
		return ""
	}
	return id
}

// Caller is the minimal RPC surface resumeMostRecentOrFresh needs; *rpc.Client
// satisfies it (same shape as go/tui.Caller, duplicated here so this package
// does not need to import go/tui just for the interface).
type Caller interface {
	Call(method string, params any, result any) error
	Close() error
}

// ensureDaemonReachable dials the daemon socket, auto-starting carina-daemon
// and retrying with backoff if the first dial reports the daemon
// unreachable (P1.5(a)'s "auto-start on unreachable"). Any other dial
// failure (e.g. a malformed socket path) is returned immediately.
//
// Core spawn/ownership lives in go/localdaemon (shared with carina-tui).
// Hooks above remain so existing CLI unit tests can inject dial/spawn failures.
func ensureDaemonReachable(socket string) (*rpcClient, error) {
	// Temporarily bind package hooks so tests that replace dialSocketHook /
	// spawnDaemonHook still observe CLI-level control.
	origDial, origSpawn, origDeadline := localdaemon.Dial, localdaemon.Spawn, localdaemon.ReachableDeadline
	localdaemon.Dial = dialSocketHook
	localdaemon.Spawn = func(sock string) error {
		// spawnDaemonHook is socket-free (historical CLI API); resolve here.
		_ = sock
		return spawnDaemonHook()
	}
	localdaemon.ReachableDeadline = daemonReachableDeadline
	defer func() {
		localdaemon.Dial, localdaemon.Spawn, localdaemon.ReachableDeadline = origDial, origSpawn, origDeadline
	}()
	return localdaemon.EnsureReachable(socket)
}

// daemonStartupBackoff mirrors go/localdaemon retry cadence (kept for CLI unit tests).
func daemonStartupBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond * time.Duration(attempt+1)
	if d > time.Second {
		d = time.Second
	}
	return d
}
