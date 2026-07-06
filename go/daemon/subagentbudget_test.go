package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSubagentTokenBudget: a subagent is bounded by the same per-task token
// budget, so a delegated task can't run away either.
func TestSubagentTokenBudget(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.maxTaskTokens = 10 // tiny: the first subagent prompt blows it

	agentsDir := filepath.Join(ws, ".carina", "agents")
	os.MkdirAll(agentsDir, 0o755)
	os.WriteFile(filepath.Join(agentsDir, "scout.md"),
		[]byte("---\nname: scout\ndescription: recon\nprofile: read-only\n---\nYou are a scout.\n"), 0o644)
	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"found"}`}})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	ptask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "delegate")

	summary := d.spawnSubagent(parent, ptask, "scout", "look around")
	if !strings.Contains(summary, "budget") {
		t.Fatalf("subagent should hit the token budget, got: %s", summary)
	}
}
