package daemon

import (
	"strings"
	"testing"
)

// TestAsyncSteering: a message queued for a task is drained into the agent's
// prompt at the next turn boundary (redirect a running agent without restart).
func TestAsyncSteering(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")

	// Queue a steering message before the loop runs.
	d.steer(task.TaskID, "please also add tests")

	cap := &capturingReasoner{}
	d.SetReasoner(cap)
	d.runTask(sess, task)

	if !strings.Contains(cap.lastPrompt, "please also add tests") {
		t.Fatalf("steering message should reach the agent prompt, got:\n%s", cap.lastPrompt)
	}
	// Mailbox must be drained (not re-delivered).
	if len(d.drainMailbox(task.TaskID)) != 0 {
		t.Fatal("mailbox should be empty after draining")
	}
}

func TestTaskSteerRejectsUnknownAndTerminalTasks(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")

	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"task_id": task.TaskID,
		"message": " also add tests ",
	})); err != nil {
		t.Fatalf("queued task should accept steering: %v", err)
	}
	if got := d.drainMailbox(task.TaskID); len(got) != 1 || got[0] != "also add tests" {
		t.Fatalf("steering mailbox = %#v", got)
	}

	d.sched.SetStatus(task.TaskID, "completed")
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"task_id": task.TaskID,
		"message": "too late",
	})); err == nil || !strings.Contains(err.Error(), "cannot be steered") {
		t.Fatalf("terminal task steer error = %v", err)
	}
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"task_id": "task_missing",
		"message": "hello",
	})); err == nil || !strings.Contains(err.Error(), "unknown task") {
		t.Fatalf("unknown task steer error = %v", err)
	}
}
