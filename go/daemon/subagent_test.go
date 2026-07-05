package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAttenuateChildNeverExceedsParent(t *testing.T) {
	// child requests more than parent -> clamped to parent
	if got := attenuate("read-only", "full-workspace"); got != "read-only" {
		t.Fatalf("child must not exceed parent: got %s", got)
	}
	// child requests less -> honored
	if got := attenuate("full-workspace", "read-only"); got != "read-only" {
		t.Fatalf("more-restrictive request should hold: got %s", got)
	}
	// equal
	if got := attenuate("safe-edit", "safe-edit"); got != "safe-edit" {
		t.Fatalf("equal should hold: got %s", got)
	}
	// unknown -> least privilege
	if got := attenuate("safe-edit", ""); got != "read-only" {
		t.Fatalf("empty request should default to read-only: got %s", got)
	}
}

func TestParseAgentSpec(t *testing.T) {
	md := "---\nname: scout\ndescription: fast recon\nprofile: read-only\nmodel: haiku\nmax_turns: 6\n---\nYou are a scout. Find things.\n"
	spec := parseAgentSpec(md)
	if spec == nil || spec.Name != "scout" || spec.Profile != "read-only" || spec.MaxTurns != 6 {
		t.Fatalf("bad spec: %+v", spec)
	}
	if spec.SystemPrompt == "" || spec.Description != "fast recon" {
		t.Fatalf("missing body/description: %+v", spec)
	}
}

// TestSubagentIsolatedAndAttenuated: a full-workspace parent spawns a
// read-only scout; the scout runs in its own restricted session and its
// summary returns to the parent. The scout's session must be read-only
// (attenuated) regardless of the parent.
func TestSubagentIsolatedAndAttenuated(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	// Define a project-local scout agent (read-only).
	agentsDir := filepath.Join(ws, ".carina", "agents")
	os.MkdirAll(agentsDir, 0o755)
	os.WriteFile(filepath.Join(agentsDir, "scout.md"),
		[]byte("---\nname: scout\ndescription: recon\nprofile: read-only\nmax_turns: 3\n---\nYou are a scout. Report what you find.\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "target.txt"), []byte("SECRET_MARKER here\n"), 0o600)

	// Scout's scripted behavior: search, then done.
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"search","pattern":"SECRET_MARKER"}`,
		`{"tool":"done","summary":"found SECRET_MARKER in target.txt"}`,
	}})

	// Parent session is full-workspace (permissive).
	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "delegate recon")

	summary := d.spawnSubagent(parent, parentTask, "scout", "find SECRET_MARKER")
	if summary == "" || !contains(summary, "SECRET_MARKER") {
		t.Fatalf("subagent should return its finding, got: %q", summary)
	}

	// A new child session must exist, be read-only (attenuated from parent's
	// full-workspace), and be linked to the parent at depth 1.
	var child *struct{ profile, parent string; depth int }
	for _, s := range d.store.List() {
		if s.ParentID == parent.SessionID {
			child = &struct{ profile, parent string; depth int }{s.PermissionProfile, s.ParentID, s.Depth}
		}
	}
	if child == nil {
		t.Fatal("no child session created")
	}
	if child.profile != "read-only" {
		t.Fatalf("child must be attenuated to read-only, got %s", child.profile)
	}
	if child.depth != 1 {
		t.Fatalf("child depth should be 1, got %d", child.depth)
	}
}

// TestSubagentDepthLimit: nesting is bounded.
func TestSubagentDepthLimit(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	// A session already at max depth cannot spawn.
	deep, _ := d.store.CreateSubSession(ws, "read-only", "on_request", "sess_parent", maxSubagentDepth)
	d.kern.InitSessionFull(deep.SessionID, ws, "read-only", "on_request", nil)
	task := d.sched.Submit(deep.SessionID, deep.WorkspaceID, "x")
	obs := d.executeSpawn(deep, task, &action{Tool: "spawn", Agent: "scout", Task: "y"})
	if !contains(obs, "depth") {
		t.Fatalf("spawn at max depth should be refused, got: %s", obs)
	}
}
