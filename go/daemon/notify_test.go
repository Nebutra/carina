package daemon

import (
	"sync"
	"testing"
)

// TestCompletionEnvelopeEmitted: a task reaching a terminal state publishes a
// single structured task.completed envelope with the final status and summary.
func TestCompletionEnvelopeEmitted(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	var mu sync.Mutex
	var completions []map[string]any
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "task.completed" {
			mu.Lock()
			completions = append(completions, ev)
			mu.Unlock()
		}
	})

	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"done","summary":"all set"}`,
	}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "do nothing")
	d.runTask(sess, task)

	mu.Lock()
	defer mu.Unlock()
	if len(completions) != 1 {
		t.Fatalf("expected exactly 1 completion envelope, got %d", len(completions))
	}
	env := completions[0]
	if env["status"] != "completed" {
		t.Fatalf("envelope status should be completed, got %v", env["status"])
	}
	if env["summary"] != "all set" {
		t.Fatalf("envelope summary should carry the model summary, got %v", env["summary"])
	}
	if env["task_id"] != task.TaskID {
		t.Fatalf("envelope task_id mismatch: %v", env["task_id"])
	}
	if _, ok := env["duration_ms"]; !ok {
		t.Fatal("envelope should carry duration_ms")
	}
}
