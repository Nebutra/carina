package daemon

import (
	"strings"
	"testing"
	"time"
)

// TestStructuredOutputValidation: a task requiring a JSON output with a key
// rejects a plain-text "done" and completes once the model emits valid JSON.
func TestStructuredOutputValidation(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 5 * time.Millisecond
	defer func() { retryBaseDelay = old }()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "produce json")
	d.sched.SetOutputSchema(task.TaskID, []string{"answer"})

	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"done","summary":"just some prose, not json"}`, // rejected
		`{"tool":"done","summary":"{\"answer\":42}"}`,           // accepted
	}})
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.TaskID)
	if tk.Status != "completed" {
		t.Fatalf("should complete after valid JSON, got %s", tk.Status)
	}
	if !strings.Contains(tk.Summary, "answer") {
		t.Fatalf("final summary should be the valid JSON, got %q", tk.Summary)
	}
}
