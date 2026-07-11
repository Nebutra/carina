package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTokenBudgetGovernor pauses for explicit extension rather than degrading.
func TestTokenBudgetGovernor(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.maxTaskTokens.Store(50) // tiny budget: the very first prompt blows it

	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hi\n"), 0o600)
	steps := make([]string, 40)
	for i := range steps {
		steps[i] = `{"tool":"read","path":"a.txt"}`
	}
	d.SetReasoner(&scriptedReasoner{steps: steps})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "loop")
	d.sched.SetTokenBudget(task.TaskID, 50)
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.TaskID)
	if tk.Status != "needs_input" {
		t.Fatalf("over-budget run should pause for input, got %s", tk.Status)
	}
	if !strings.Contains(tk.Summary, "budget") {
		t.Fatalf("pause reason should cite the budget, got %q", tk.Summary)
	}
	if tk.TokensUsed == 0 {
		t.Fatal("token spend should be metered")
	}
	raw, _ := json.Marshal(map[string]any{"task_id": task.TaskID, "additional_tokens": 5000, "approver": "test"})
	if _, err := d.handleTaskBudgetExtend(raw); err != nil {
		t.Fatal(err)
	}
	extended, _ := d.sched.Get(task.TaskID)
	if extended.TokenBudget != 5050 {
		t.Fatalf("budget=%d", extended.TokenBudget)
	}
}

func TestBudgetExtensionRefusesMissingCheckpointWithoutChangingState(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")
	d.sched.SetTokenBudget(task.TaskID, 10)
	d.sched.SetStatus(task.TaskID, "needs_input")
	raw, _ := json.Marshal(map[string]any{"task_id": task.TaskID, "additional_tokens": 100, "approver": "test"})
	if _, err := d.handleTaskBudgetExtend(raw); err == nil || !strings.Contains(err.Error(), "no durable checkpoint") {
		t.Fatalf("expected safe resume refusal, got %v", err)
	}
	got, _ := d.sched.Get(task.TaskID)
	if got.Status != "needs_input" || got.TokenBudget != 10 {
		t.Fatalf("failed extension mutated task: %+v", got)
	}
}
