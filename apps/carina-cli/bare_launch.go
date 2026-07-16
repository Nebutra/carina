package main

import (
	"time"

	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tuiapp"
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
// calls without touching a real unix socket.
var dialSocketHook = defaultLocalDial

// daemonReachableDeadline bounds how long ensureDaemonReachable retries the
// dial after auto-starting the daemon before giving up.
var daemonReachableDeadline = 10 * time.Second

// runBareTUI is bare `carina` (no args) via go/tuiapp.
func runBareTUI() tui.Outcome {
	return runTUI(tuiapp.Options{})
}

// runTUI launches the interactive shell. AfterDaemon wires CLI-only
// first-launch doctor; flag fields come from `carina -session …` etc.
func runTUI(opts tuiapp.Options) tui.Outcome {
	if opts.AfterDaemon == nil {
		opts.AfterDaemon = func(call tuiapp.RPC) {
			maybeAutoRunDoctor(call)
		}
	}
	// Bind CLI dial/spawn test hooks into localdaemon for the duration of
	// launch (same as ensureDaemonReachable), so unit tests that stub
	// dialSocketHook/spawnDaemonHook still control reachability.
	origDial, origSpawn, origDeadline := localdaemon.Dial, localdaemon.Spawn, localdaemon.ReachableDeadline
	localdaemon.Dial = dialSocketHook
	localdaemon.Spawn = func(sock string) error {
		_ = sock
		return spawnDaemonHook()
	}
	localdaemon.ReachableDeadline = daemonReachableDeadline
	defer func() {
		localdaemon.Dial, localdaemon.Spawn, localdaemon.ReachableDeadline = origDial, origSpawn, origDeadline
	}()
	return tuiapp.Run(opts)
}

// Caller is the minimal RPC surface doctor / session helpers need.
type Caller interface {
	Call(method string, params any, result any) error
	Close() error
}

// ensureDaemonReachable dials the daemon socket, auto-starting carina-daemon
// when unreachable. Used by `carina daemon start` and other CLI paths.
func ensureDaemonReachable(socket string) (*rpcClient, error) {
	origDial, origSpawn, origDeadline := localdaemon.Dial, localdaemon.Spawn, localdaemon.ReachableDeadline
	localdaemon.Dial = dialSocketHook
	localdaemon.Spawn = func(sock string) error {
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

// resolveBareTUILocale kept for bare_invocation_test locale precedence.
func resolveBareTUILocale(configLocale string) (string, error) {
	return microcopy.ResolveLocale("", configLocale)
}
