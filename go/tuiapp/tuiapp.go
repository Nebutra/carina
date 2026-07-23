// Package tuiapp is the single interactive launch path for Carina.
//
// Operator entry (single binary, single shell):
//
//	carina                 # bare, on a TTY
//	carina [shell flags]   # same shell with -session / -workspace / …
//
// Auto-starts carina-daemon when the socket is down; UI is go/tui.
package tuiapp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/localruntime"
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
	// Stderr receives non-interactive gate and terminal failures (default os.Stderr).
	Stderr io.Writer
	// AfterDaemon runs once the daemon is reachable and before the TUI
	// attaches (e.g. first-launch doctor). Failures must not abort launch.
	AfterDaemon func(call RPC)
	// RequireTTY refuses non-interactive stdio (flag-form `carina -…` sets true).
	// Bare no-arg `carina` decides TTY before calling Run.
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
	interactive := isTTY(os.Stdout) && isTTY(os.Stdin)

	// Non-interactive callers cannot enter BootstrapScreen, so preserve the
	// fail-closed locale result for pipes and tests. Interactive callers see the
	// same validation failure inside the bootstrap identity stage.
	if value := os.Getenv("CARINA_LOCALE"); !interactive && opts.Locale == "" && value != "" {
		if _, err := microcopy.CanonicalLocale(value); err != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage
		}
	}

	if opts.RequireTTY && (!isTTY(os.Stdout) || !isTTY(os.Stdin)) {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapInteractiveRequired, nil, bootstrapLocale))
		return tui.OutcomeUsage
	}

	prepared, bootstrapOutcome, err := runBootstrap(opts, bootstrapLocale, os.Stdin, os.Stdout, interactive && !opts.NoAltScreen)
	if errors.Is(err, errRuntimeModeChoiceCancelled) {
		return bootstrapOutcome
	}
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	home := prepared.home
	projectRoot := prepared.projectRoot
	cfg := prepared.config
	socket := prepared.socket
	runtimeSpec := prepared.runtimeSpec
	loc := prepared.locale
	keybindings := prepared.keybindings
	sessionID := prepared.sessionID

	th := theme.New(theme.Detect(os.Getenv, true))
	initialTarget := tui.ConnectionTarget{
		Socket: socket, SessionID: sessionID, WorkspaceRoot: projectRoot, StateDir: cfg.StateDir,
		RuntimeSpec: runtimeSpec, AutoStart: true,
	}
	connectionController := tui.NewConnectionController(initialTarget)
	coordinator := newRuntimeCoordinator(home, projectRoot, connectionController)
	defer coordinator.close()
	model, err := tui.NewChecked(tui.Options{
		Theme:             th,
		Locale:            loc,
		Socket:            socket,
		SessionID:         sessionID,
		WorkspaceRoot:     projectRoot,
		StateDir:          cfg.StateDir,
		Keybindings:       keybindings,
		NoAlternateScreen: opts.NoAltScreen || strings.EqualFold(cfg.TUIAlternateScreen, "never"),
		KeymapUpdater:     coordinator.keymapUpdater(),
		SwitchSession:     connectionController.Switch,
		PrepareTarget:     coordinator.prepare,
		CommitTarget:      coordinator.commit,
		AbortTarget:       coordinator.abort,
		AcknowledgeTarget: coordinator.acknowledge,
		ListWorkspaces:    coordinator.listWorkspaces,
		LoadWorkspace:     workspaceLoader(home),
		ResumeWorkspace:   workspaceResumer,
		OnFirstConnection: func(call tui.Caller) {
			if opts.AfterDaemon != nil {
				opts.AfterDaemon(borrowedRPC{Caller: call})
			}
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage
	}
	defer model.Close()
	prog := tea.NewProgram(model)
	coordinator.startWatcher(prog)
	if runtimeSpec != nil {
		tui.ConnectControlledRuntime(prog, socket, sessionID, projectRoot, connectionController, *runtimeSpec)
	} else {
		tui.ConnectControlled(prog, socket, sessionID, projectRoot, connectionController)
	}

	final, err := prog.Run()
	if raw := model.CloseTerminalGraphics(); raw != "" {
		_, _ = fmt.Fprint(os.Stdout, raw)
	}
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapRuntimeFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeRuntimeError
	}
	if m, ok := final.(*tui.Model); ok {
		return m.Outcome()
	}
	return tui.OutcomeOK
}

type borrowedRPC struct{ tui.Caller }

func (borrowedRPC) Close() error { return nil }

var errRuntimeModeChoiceCancelled = errors.New("runtime mode choice cancelled")

func workspaceLoader(home string) func(string) (tui.WorkspaceDestination, error) {
	return func(root string) (tui.WorkspaceDestination, error) {
		resolution, err := localruntime.Resolve(home, root, localruntime.ModeWorkspace)
		if err != nil {
			return tui.WorkspaceDestination{}, err
		}
		client, _, err := localdaemon.ConnectOrStart(resolution.Spec)
		if err != nil {
			return tui.WorkspaceDestination{}, err
		}
		defer client.Close()
		var sessions []tui.SessionListItem
		if err := client.Call("session.list", map[string]any{}, &sessions); err != nil {
			return tui.WorkspaceDestination{}, err
		}
		return tui.WorkspaceDestination{
			Target: tui.ConnectionTarget{
				Socket: resolution.Spec.Paths.SocketPath, WorkspaceRoot: resolution.Workspace.CanonicalRoot,
				StateDir: resolution.Config.StateDir, RuntimeSpec: &resolution.Spec, AutoStart: true,
			},
			Sessions: sessions,
		}, nil
	}
}

func workspaceResumer(target tui.ConnectionTarget, sessionID string) (tui.ConnectionTarget, error) {
	if target.RuntimeSpec == nil {
		return tui.ConnectionTarget{}, fmt.Errorf("workspace runtime identity is required")
	}
	client, _, err := localdaemon.ConnectOrStart(*target.RuntimeSpec)
	if err != nil {
		return tui.ConnectionTarget{}, err
	}
	defer client.Close()
	var resumed tui.SessionListItem
	if err := client.Call("session.resume", map[string]any{"session_id": sessionID}, &resumed); err != nil {
		return tui.ConnectionTarget{}, err
	}
	if resumed.SessionID == "" {
		return tui.ConnectionTarget{}, fmt.Errorf("session resume returned no session id")
	}
	if resumed.WorkspaceRoot != "" && filepath.Clean(resumed.WorkspaceRoot) != filepath.Clean(target.WorkspaceRoot) {
		return tui.ConnectionTarget{}, fmt.Errorf("session workspace mismatch")
	}
	target.SessionID = resumed.SessionID
	return target, nil
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
