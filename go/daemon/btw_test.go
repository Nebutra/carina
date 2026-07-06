package daemon

import (
	"testing"
)

// TestBtwSideQueryIsEphemeral: a /btw side-query returns an answer without
// changing the task's status or transcript.
func TestBtwSideQueryIsEphemeral(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{"6 x 7 = 42."}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "main task")

	res, err := d.handleTaskBtw(mustJSON(t, map[string]any{
		"task_id": task.TaskID, "question": "what is 6x7?"}))
	if err != nil {
		t.Fatalf("btw: %v", err)
	}
	m := res.(map[string]any)
	if m["answer"] != "6 x 7 = 42." {
		t.Fatalf("unexpected side-query answer: %v", m["answer"])
	}
	if m["ephemeral"] != true {
		t.Fatal("side-query must be marked ephemeral")
	}
	// The main task is untouched: still queued, no checkpoint written.
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "queued" {
		t.Fatalf("side-query must not change task status, got %s", tk.Status)
	}
	if cp := d.runs.loadCheckpoint(task.TaskID); cp != nil {
		t.Fatal("side-query must not create a transcript checkpoint")
	}
}

func TestBtwValidation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{"x"}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "t")

	if _, err := d.handleTaskBtw(mustJSON(t, map[string]any{"task_id": task.TaskID, "question": "  "})); err == nil {
		t.Fatal("an empty question must error")
	}
	if _, err := d.handleTaskBtw(mustJSON(t, map[string]any{"task_id": "nope", "question": "hi"})); err == nil {
		t.Fatal("an unknown task must error")
	}
}
