package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

// TestEnsureDaemonReachableSkipsSpawnWhenFirstDialSucceeds pins the common
// path: if the daemon is already up, ensureDaemonReachable must not touch
// spawnDaemonHook at all and must return the first successful dial.
func TestEnsureDaemonReachableSkipsSpawnWhenFirstDialSucceeds(t *testing.T) {
	dialCalls := 0
	origDial := dialSocketHook
	dialSocketHook = func(socket string) (*rpcClient, error) {
		dialCalls++
		if socket != "/tmp/example.sock" {
			t.Fatalf("dialSocketHook called with %q, want %q", socket, "/tmp/example.sock")
		}
		return &rpc.Client{}, nil
	}
	defer func() { dialSocketHook = origDial }()

	spawnCalls := 0
	origSpawn := spawnDaemonHook
	spawnDaemonHook = func() error { spawnCalls++; return nil }
	defer func() { spawnDaemonHook = origSpawn }()

	c, err := ensureDaemonReachable("/tmp/example.sock")
	if err != nil {
		t.Fatalf("ensureDaemonReachable: unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("ensureDaemonReachable returned nil client on success")
	}
	if dialCalls != 1 {
		t.Fatalf("dialSocketHook called %d times, want 1", dialCalls)
	}
	if spawnCalls != 0 {
		t.Fatalf("spawnDaemonHook called %d times, want 0 (daemon was already reachable)", spawnCalls)
	}
}

// TestEnsureDaemonReachableReturnsNonUnreachableErrorImmediately asserts
// that a dial failure NOT wrapping rpc.ErrDaemonUnreachable (e.g. a
// malformed socket path) is returned as-is, without ever invoking
// spawnDaemonHook or retrying.
func TestEnsureDaemonReachableReturnsNonUnreachableErrorImmediately(t *testing.T) {
	wantErr := errors.New("boom: not a daemon-unreachable error")
	dialCalls := 0
	origDial := dialSocketHook
	dialSocketHook = func(socket string) (*rpcClient, error) {
		dialCalls++
		return nil, wantErr
	}
	defer func() { dialSocketHook = origDial }()

	spawnCalls := 0
	origSpawn := spawnDaemonHook
	spawnDaemonHook = func() error { spawnCalls++; return nil }
	defer func() { spawnDaemonHook = origSpawn }()

	_, err := ensureDaemonReachable("/tmp/example.sock")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ensureDaemonReachable error = %v, want it to wrap %v", err, wantErr)
	}
	if dialCalls != 1 {
		t.Fatalf("dialSocketHook called %d times, want exactly 1 (no retry for a non-unreachable error)", dialCalls)
	}
	if spawnCalls != 0 {
		t.Fatalf("spawnDaemonHook called %d times, want 0 (must not auto-start for a non-unreachable dial error)", spawnCalls)
	}
}

// TestEnsureDaemonReachableAutoStartsAndRetriesOnUnreachable is P1.5(a)'s
// core contract: when the first dial reports the daemon unreachable,
// ensureDaemonReachable must auto-start it via spawnDaemonHook and then
// retry the dial (through the same seam) until it succeeds, returning the
// first successful client.
func TestEnsureDaemonReachableAutoStartsAndRetriesOnUnreachable(t *testing.T) {
	dialCalls := 0
	const failuresBeforeSuccess = 3
	origDial := dialSocketHook
	dialSocketHook = func(socket string) (*rpcClient, error) {
		dialCalls++
		if dialCalls <= failuresBeforeSuccess {
			return nil, fmt.Errorf("dial %d: %w", dialCalls, rpc.ErrDaemonUnreachable)
		}
		return &rpc.Client{}, nil
	}
	defer func() { dialSocketHook = origDial }()

	spawnCalls := 0
	origSpawn := spawnDaemonHook
	spawnDaemonHook = func() error { spawnCalls++; return nil }
	defer func() { spawnDaemonHook = origSpawn }()

	c, err := ensureDaemonReachable("/tmp/example.sock")
	if err != nil {
		t.Fatalf("ensureDaemonReachable: unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("ensureDaemonReachable returned nil client on eventual success")
	}
	if spawnCalls != 1 {
		t.Fatalf("spawnDaemonHook called %d times, want exactly 1 (auto-start happens once, not once per retry)", spawnCalls)
	}
	// One initial dial (fails unreachable) + failuresBeforeSuccess retries
	// after spawn, the last of which succeeds.
	if dialCalls != 1+failuresBeforeSuccess {
		t.Fatalf("dialSocketHook called %d times, want %d (1 initial + %d retries)", dialCalls, 1+failuresBeforeSuccess, failuresBeforeSuccess)
	}
}

// TestEnsureDaemonReachablePropagatesSpawnFailure asserts that a failure to
// auto-start the daemon (spawnDaemonHook returning an error) is surfaced
// immediately, without ever entering the retry loop.
func TestEnsureDaemonReachablePropagatesSpawnFailure(t *testing.T) {
	origDial := dialSocketHook
	dialSocketHook = func(socket string) (*rpcClient, error) {
		return nil, fmt.Errorf("dial: %w", rpc.ErrDaemonUnreachable)
	}
	defer func() { dialSocketHook = origDial }()

	spawnErr := errors.New("exec: carina-daemon not found")
	spawnCalls := 0
	origSpawn := spawnDaemonHook
	spawnDaemonHook = func() error { spawnCalls++; return spawnErr }
	defer func() { spawnDaemonHook = origSpawn }()

	_, err := ensureDaemonReachable("/tmp/example.sock")
	if err == nil {
		t.Fatal("ensureDaemonReachable: expected an error when spawnDaemonHook fails")
	}
	if !errors.Is(err, spawnErr) {
		t.Fatalf("ensureDaemonReachable error = %v, want it to wrap the spawn error %v", err, spawnErr)
	}
	if spawnCalls != 1 {
		t.Fatalf("spawnDaemonHook called %d times, want exactly 1", spawnCalls)
	}
}

// TestEnsureDaemonReachableGivesUpAfterDeadline asserts the 10-second
// deadline exhaustion path: if every post-spawn retry keeps failing with
// ErrDaemonUnreachable, ensureDaemonReachable must eventually give up and
// return an error wrapping the last dial failure — not retry forever. This
// drives the retry loop through the real (short, linear, sub-second)
// daemonStartupBackoff deltas via the dialSocketHook seam, so the test
// itself stays well under the 10s production deadline while still proving
// termination.
func TestEnsureDaemonReachableGivesUpAfterDeadline(t *testing.T) {
	dialCalls := 0
	lastErr := fmt.Errorf("dial: %w", rpc.ErrDaemonUnreachable)
	origDial := dialSocketHook
	dialSocketHook = func(socket string) (*rpcClient, error) {
		dialCalls++
		return nil, lastErr
	}
	defer func() { dialSocketHook = origDial }()

	origSpawn := spawnDaemonHook
	spawnDaemonHook = func() error { return nil }
	defer func() { spawnDaemonHook = origSpawn }()

	origDeadline := daemonReachableDeadline
	daemonReachableDeadline = 200 * time.Millisecond
	defer func() { daemonReachableDeadline = origDeadline }()

	start := time.Now()
	_, err := ensureDaemonReachable("/tmp/example.sock")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("ensureDaemonReachable: expected an error after the deadline elapses")
	}
	if !errors.Is(err, rpc.ErrDaemonUnreachable) {
		t.Fatalf("ensureDaemonReachable error = %v, want it to wrap ErrDaemonUnreachable (the last dial failure)", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ensureDaemonReachable took %v to give up, want well under its shortened deadline (bounded, not indefinite)", elapsed)
	}
	if dialCalls < 2 {
		t.Fatalf("dialSocketHook called %d times, want at least 2 (initial dial + at least one retry before the deadline)", dialCalls)
	}
}

// TestDaemonStartupBackoffIsShortLinearAndBounded pins
// daemonStartupBackoff's documented shape directly: short, linear, capped
// at 1s -- the retry cadence ensureDaemonReachable relies on to stay well
// under its 10s deadline against a freshly spawned daemon.
func TestDaemonStartupBackoffIsShortLinearAndBounded(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{9, time.Second},  // 1000ms, right at the cap
		{20, time.Second}, // would be 2100ms uncapped; must clamp to 1s
	}
	for _, tc := range cases {
		got := daemonStartupBackoff(tc.attempt)
		if got != tc.want {
			t.Errorf("daemonStartupBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}
