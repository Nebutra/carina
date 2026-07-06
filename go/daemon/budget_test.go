package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTokenBudgetGovernor: a run that exceeds its per-task token budget degrades
// gracefully (rather than looping and burning spend), and the spend is metered.
func TestTokenBudgetGovernor(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.maxTaskTokens = 50 // tiny budget: the very first prompt blows it

	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hi\n"), 0o600)
	steps := make([]string, 40)
	for i := range steps {
		steps[i] = `{"tool":"read","path":"a.txt"}`
	}
	d.SetReasoner(&scriptedReasoner{steps: steps})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "loop")
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.TaskID)
	if tk.Status != "degraded" {
		t.Fatalf("over-budget run should degrade, got %s", tk.Status)
	}
	if !strings.Contains(tk.Summary, "budget") {
		t.Fatalf("degrade reason should cite the budget, got %q", tk.Summary)
	}
	if tk.TokensUsed == 0 {
		t.Fatal("token spend should be metered")
	}
}
