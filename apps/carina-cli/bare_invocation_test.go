package main

import "testing"

// TestDecideBareInvocationLaunchesTUIOnRealTTY pins P1.5(a)'s decision: bare
// `carina` (no args) launches the interactive TUI in-process when both
// stdin and stdout are a real terminal — no "crn" subcommand, "carina"
// alone behaves like "claude" launching Claude Code. This tests the
// decision logic directly (a pure function of two bools), not an actual
// PTY — the interactive smoke test belongs in a separate PTY-integration
// test, per the task's explicit instruction to test the decision
// separately from the interactive PTY smoke test.
func TestDecideBareInvocationLaunchesTUIOnRealTTY(t *testing.T) {
	got := decideBareInvocation(true, true)
	if got != bareActionLaunchTUI {
		t.Fatalf("decideBareInvocation(tty, tty) = %v, want bareActionLaunchTUI", got)
	}
}

// TestDecideBareInvocationPrintsUsageWhenPiped preserves today's behavior
// for scripts/pipes that invoke bare `carina` by accident: if either stdin
// or stdout is not a TTY, print usage and exit 2.
func TestDecideBareInvocationPrintsUsageWhenPiped(t *testing.T) {
	cases := []struct {
		name                string
		stdinTTY, stdoutTTY bool
	}{
		{"neither tty", false, false},
		{"stdin piped only", false, true},
		{"stdout piped only", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideBareInvocation(tc.stdinTTY, tc.stdoutTTY)
			if got != bareActionUsage {
				t.Fatalf("decideBareInvocation(%v, %v) = %v, want bareActionUsage", tc.stdinTTY, tc.stdoutTTY, got)
			}
		})
	}
}

// TestLatestSessionForWorkspaceFiltersByCwd pins the "most recent in this
// repo, not globally" requirement: a newer session in a DIFFERENT workspace
// must not shadow an older session in the current workspace.
func TestLatestSessionForWorkspaceFiltersByCwd(t *testing.T) {
	sessions := []sessionSummary{
		{SessionID: "sess_other_new", WorkspaceRoot: "/repo/other", CreatedAt: "2026-07-09T12:00:00Z"},
		{SessionID: "sess_here_old", WorkspaceRoot: "/repo/here", CreatedAt: "2026-07-01T00:00:00Z"},
		{SessionID: "sess_here_new", WorkspaceRoot: "/repo/here", CreatedAt: "2026-07-08T00:00:00Z"},
	}
	id, ok := latestSessionForWorkspace(sessions, "/repo/here")
	if !ok {
		t.Fatal("expected a session for /repo/here")
	}
	if id != "sess_here_new" {
		t.Fatalf("latestSessionForWorkspace = %q, want %q (must not pick the newer session from a different workspace)", id, "sess_here_new")
	}
}

// TestLatestSessionForWorkspaceNoMatchFallsThrough asserts the "none found
// for cwd -> fall through to session.create" contract: ok must be false
// when no session matches, not a zero-value/garbage session id.
func TestLatestSessionForWorkspaceNoMatchFallsThrough(t *testing.T) {
	sessions := []sessionSummary{
		{SessionID: "sess_elsewhere", WorkspaceRoot: "/repo/elsewhere", CreatedAt: "2026-07-09T12:00:00Z"},
	}
	id, ok := latestSessionForWorkspace(sessions, "/repo/here")
	if ok {
		t.Fatalf("expected ok=false when no session matches cwd, got id=%q", id)
	}
}
