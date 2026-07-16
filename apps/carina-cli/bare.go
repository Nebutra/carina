package main

// bareAction is the outcome of deciding what a bare `carina` invocation (no
// subcommand) should do. This is a pure decision, deliberately separated
// from actually launching Bubble Tea or dialing the daemon, so it is
// testable without a real PTY or socket (P1.5(a)).
type bareAction int

const (
	// bareActionUsage prints usage and exits 2 — preserved behavior for
	// scripts/pipes that invoke bare `carina` by accident (today's only
	// behavior for len(os.Args) < 2).
	bareActionUsage bareAction = iota
	// bareActionLaunchTUI launches the interactive TUI in-process
	// (go/tuiapp; no second binary).
	bareActionLaunchTUI
)

// decideBareInvocation is the pure decision function for bare `carina` (no
// arguments): TTY on both stdin and stdout launches the TUI; anything else
// (piped/redirected) preserves today's usage+exit-2 behavior.
func decideBareInvocation(stdinIsTTY, stdoutIsTTY bool) bareAction {
	if stdinIsTTY && stdoutIsTTY {
		return bareActionLaunchTUI
	}
	return bareActionUsage
}

// sessionSummary is the minimal shape latestSessionForWorkspace needs from
// session.list.
type sessionSummary struct {
	SessionID     string `json:"session_id"`
	WorkspaceRoot string `json:"workspace_root"`
	CreatedAt     string `json:"created_at"`
}

// latestSessionForWorkspace narrows latestSession's max-by-CreatedAt pattern
// to sessions whose WorkspaceRoot matches cwd — "most recent" for bare
// `carina` means "most recent in this repo", not globally across every
// workspace the user ever ran carina in. Returns ok=false when no session
// for cwd exists (the caller should fall through to session.create,
// exactly as `carina run` does today).
func latestSessionForWorkspace(sessions []sessionSummary, cwd string) (id string, ok bool) {
	var latest sessionSummary
	found := false
	for _, s := range sessions {
		if s.WorkspaceRoot != cwd {
			continue
		}
		if !found || s.CreatedAt > latest.CreatedAt {
			latest = s
			found = true
		}
	}
	if !found {
		return "", false
	}
	return latest.SessionID, true
}
