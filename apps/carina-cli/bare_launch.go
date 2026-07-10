package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tui/theme"

	tea "charm.land/bubbletea/v2"
)

// spawnDaemonHook lets tests observe/replace the actual daemon spawn without
// starting a real process (mirrors dialHook's seam in client.go).
var spawnDaemonHook = spawnDaemon

// dialSocketHook lets tests observe/replace ensureDaemonReachable's dial
// calls without touching a real unix socket (mirrors dialHook's seam in
// client.go). Unlike dialHook, this takes an explicit socket path — the
// same shape as rpc.Dial and ensureDaemonReachable itself — since
// ensureDaemonReachable is handed its socket path by its caller rather than
// resolving it internally.
var dialSocketHook = rpc.Dial

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
	socket, err := defaultSocketPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "carina: %v\n", err)
		return tui.OutcomeRuntimeError
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "carina: %v\n", err)
		return tui.OutcomeRuntimeError
	}

	call, err := ensureDaemonReachable(socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "carina: %v\n", err)
		return tui.OutcomeDaemonUnreachable
	}

	maybeAutoRunDoctor(call)

	sessionID := resumeMostRecentOrFresh(call, cwd)
	call.Close()

	loc := microcopy.DetectLocale()
	th := theme.New(theme.Detect(os.Getenv, true))
	model := tui.New(tui.Options{
		Theme:         th,
		Locale:        loc,
		Socket:        socket,
		SessionID:     sessionID,
		WorkspaceRoot: cwd,
	})
	prog := tea.NewProgram(model)
	tui.Connect(prog, socket, sessionID, cwd)

	final, err := prog.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "carina: %v\n", err)
		return tui.OutcomeRuntimeError
	}
	if m, ok := final.(*tui.Model); ok {
		return m.Outcome()
	}
	return tui.OutcomeOK
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
func ensureDaemonReachable(socket string) (*rpcClient, error) {
	c, err := dialSocketHook(socket)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, rpc.ErrDaemonUnreachable) {
		return nil, err
	}

	if spawnErr := spawnDaemonHook(); spawnErr != nil {
		return nil, fmt.Errorf("daemon unreachable and auto-start failed: %w", spawnErr)
	}

	deadline := time.Now().Add(daemonReachableDeadline)
	var lastErr error = err
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		time.Sleep(daemonStartupBackoff(attempt))
		c, err := dialSocketHook(socket)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("daemon did not become reachable after auto-start: %w", lastErr)
}

// daemonStartupBackoff returns the delay before the (attempt+1)-th reachability
// probe after auto-starting the daemon: short and linear, since a freshly
// spawned daemon binds its socket in well under a second in practice.
func daemonStartupBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond * time.Duration(attempt+1)
	if d > time.Second {
		d = time.Second
	}
	return d
}

// spawnDaemon starts carina-daemon detached from this process (the
// documented `carina-daemon &` idiom): stdio redirected to /dev/null,
// process group detached from the terminal, not waited on — bare `carina`
// hands off and moves on to dialing.
func spawnDaemon() error {
	bin := "carina-daemon"
	if dir := toolsDir(); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "carina-daemon")); err == nil {
			bin = filepath.Join(dir, "carina-daemon")
		}
	}
	cmd := exec.Command(bin)
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	if err := cmd.Start(); err != nil {
		_ = devnull.Close()
		return fmt.Errorf("start %s: %w", bin, err)
	}
	// Release the child so it survives this process without becoming a
	// zombie once it exits; carina-daemon is a long-running control-plane
	// process, not something bare `carina` supervises.
	return cmd.Process.Release()
}
