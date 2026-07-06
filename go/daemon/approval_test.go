package daemon

import (
	"testing"
	"time"
)

// awaitPermissionRequest returns a channel that receives the decision_id of the
// next permission.request envelope.
func permissionRequests(d *Daemon) <-chan string {
	ch := make(chan string, 4)
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "permission.request" {
			if id, ok := ev["decision_id"].(string); ok {
				ch <- id
			}
		}
	})
	return ch
}

func TestInteractiveApprovalAllowAndDeny(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	reqs := permissionRequests(d)

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)

	// --- ALLOW path ---
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)
	if dec.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %s", dec.Decision)
	}
	out := make(chan bool, 1)
	go func() {
		_, ok := d.resolveApproval(sess, task, dec, "npm install left-pad")
		out <- ok
	}()
	select {
	case id := <-reqs:
		if id != dec.DecisionID {
			t.Fatalf("permission.request decision_id mismatch: %s vs %s", id, dec.DecisionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no permission.request emitted")
	}
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "waiting_approval" {
		t.Fatalf("task should pause at waiting_approval, got %s", tk.Status)
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{
		"decision_id": dec.DecisionID, "approve": true})); err != nil {
		t.Fatal(err)
	}
	if ok := <-out; !ok {
		t.Fatal("an approved decision must resolve to allowed")
	}

	// --- DENY path ---
	task2 := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run2")
	dec2, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install right-pad", task2.TaskID)
	out2 := make(chan bool, 1)
	go func() {
		_, ok := d.resolveApproval(sess, task2, dec2, "npm install right-pad")
		out2 <- ok
	}()
	<-reqs
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{
		"decision_id": dec2.DecisionID, "approve": false})); err != nil {
		t.Fatal(err)
	}
	if ok := <-out2; ok {
		t.Fatal("a denied decision must not resolve to allowed")
	}
}

func TestInteractiveApprovalTimeoutDenies(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 150 * time.Millisecond // no operator will answer

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)

	if _, ok := d.resolveApproval(sess, task, dec, "npm install left-pad"); ok {
		t.Fatal("an unanswered approval must time out to denied")
	}
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "running" {
		t.Fatalf("task should return to running after timeout, got %s", tk.Status)
	}
}

// Autonomous mode (default) must keep auto-approving — no pause, no request.
func TestAutonomousApprovalUnchanged(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	reqs := permissionRequests(d)

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)

	_, ok := d.resolveApproval(sess, task, dec, "npm install left-pad")
	if !ok {
		t.Fatal("autonomous mode must auto-approve requires_approval")
	}
	select {
	case <-reqs:
		t.Fatal("autonomous mode must not emit a permission.request")
	default:
	}
}
