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
