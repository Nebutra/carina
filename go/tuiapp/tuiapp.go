// Package tuiapp is the single interactive launch path for Carina.
//
// Operator entries (no separate carina-tui binary):
//
//	carina            # bare, on a TTY → this package
//	carina tui [...]  # explicit flags, same path
//
// Both auto-start carina-daemon when the socket is down and share one
// Bubble Tea model (go/tui).
package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tui/theme"
)

// Options configures the interactive shell. Zero values pick sensible
// defaults (default socket, cwd workspace, config locale, auto session).
type Options struct {
	Socket        string
	SessionID     string // empty → pending / last-active / workspace-recent / fresh
	WorkspaceRoot string // empty → cwd
	Locale        string // flag locale; empty → config / env / system
	NoAltScreen   bool
	// Stderr receives bootstrap / recovery messages (default os.Stderr).
	Stderr io.Writer
	// AfterDaemon runs once the daemon is reachable and before the TUI
	// attaches (e.g. first-launch doctor). Failures must not abort launch.
	AfterDaemon func(call RPC)
	// RequireTTY refuses non-interactive stdio (`carina tui` sets true).
	// Bare `carina` decides TTY before calling Run.
	RequireTTY bool
}

// RPC is the minimal dial surface AfterDaemon / session resolution need.
type RPC interface {
	Call(method string, params any, result any) error
	Close() error
}

// Run starts the interactive TUI and returns its governance outcome.
func Run(opts Options) tui.Outcome {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	bootstrapLocale := microcopy.DetectBootstrapLocale()

	// Validate explicit env locale before the TTY gate so
	// CARINA_LOCALE=de fails closed with a locale error even under go test
	// (non-TTY).
	if value := os.Getenv("CARINA_LOCALE"); opts.Locale == "" && value != "" {
		if _, err := microcopy.CanonicalLocale(value); err != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage
		}
	}

	if opts.RequireTTY && (!isTTY(os.Stdout) || !isTTY(os.Stdin)) {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapInteractiveRequired, nil, bootstrapLocale))
		return tui.OutcomeUsage
	}

	socket := strings.TrimSpace(opts.Socket)
	if socket == "" {
		socket = defaultSocket()
	}
	projectRoot := strings.TrimSpace(opts.WorkspaceRoot)
	if projectRoot == "" {
		var err error
		projectRoot, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
			return tui.OutcomeRuntimeError
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapResolveHomeFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	cfg, err := config.Load(home, projectRoot)
	if err != nil {
		if errors.Is(err, config.ErrInvalidTUILocale) {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage
		}
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	loc, err := microcopy.ResolveLocale(opts.Locale, cfg.TUILocale)
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeUsage
	}
	keybindings, err := tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage
	}

	call, err := localdaemon.EnsureReachable(socket)
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeDaemonUnreachable
	}
	if opts.AfterDaemon != nil {
		opts.AfterDaemon(call)
	}

	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		sessionID, err = resolveSession(call, cfg.StateDir, projectRoot)
		if err != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapRecoveryFailed, microcopy.Args{"reason": err.Error()}, loc))
			_ = call.Close()
			return tui.OutcomeRuntimeError
		}
	}
	_ = call.Close()

	th := theme.New(theme.Detect(os.Getenv, true))
	connectionController := tui.NewConnectionController()
	model, err := tui.NewChecked(tui.Options{
		Theme:             th,
		Locale:            loc,
		Socket:            socket,
		SessionID:         sessionID,
		WorkspaceRoot:     projectRoot,
		StateDir:          cfg.StateDir,
		Keybindings:       keybindings,
		NoAlternateScreen: opts.NoAltScreen || strings.EqualFold(cfg.TUIAlternateScreen, "never"),
		KeymapUpdater:     keymapUpdater(home, projectRoot),
		SwitchSession:     connectionController.Switch,
	})
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage
	}
	defer model.Close()
	prog := tea.NewProgram(model)
	watchCtx, stopWatching := context.WithCancel(context.Background())
	defer stopWatching()
	go tui.WatchKeybindings(watchCtx, home, projectRoot, prog)
	tui.ConnectControlled(prog, socket, sessionID, projectRoot, connectionController)

	final, err := prog.Run()
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapRuntimeFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeRuntimeError
	}
	if m, ok := final.(*tui.Model); ok {
		return m.Outcome()
	}
	return tui.OutcomeOK
}

func resolveSession(call RPC, stateDir, projectRoot string) (string, error) {
	sessionID, err := tui.LatestPendingSubmissionSession(stateDir, projectRoot)
	if err != nil {
		return "", err
	}
	if sessionID != "" {
		return sessionID, nil
	}
	sessionID, err = tui.LastActiveSession(stateDir, projectRoot)
	if err != nil {
		return "", err
	}
	if sessionID != "" {
		return sessionID, nil
	}
	// Fall through to daemon session.list for this workspace (bare-carina contract).
	if call == nil {
		return "", nil
	}
	var sessions []struct {
		SessionID     string `json:"session_id"`
		WorkspaceRoot string `json:"workspace_root"`
		CreatedAt     string `json:"created_at"`
	}
	if err := call.Call("session.list", map[string]any{}, &sessions); err != nil {
		return "", nil // degrade to fresh session
	}
	var latest string
	var latestAt string
	for _, s := range sessions {
		if s.WorkspaceRoot != projectRoot {
			continue
		}
		if latest == "" || s.CreatedAt > latestAt {
			latest = s.SessionID
			latestAt = s.CreatedAt
		}
	}
	return latest, nil
}

func keymapUpdater(home, projectRoot string) tui.KeymapUpdateFunc {
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

func defaultSocket() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".carina", "daemon.sock")
	}
	return filepath.Join(home, ".carina", "daemon.sock")
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Ensure rpc.Client satisfies RPC.
var _ RPC = (*rpc.Client)(nil)
