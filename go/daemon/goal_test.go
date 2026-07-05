package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TsekaLuk/pi-os/go/scheduler"
)

// TestGoalApprovalModeAxis proves the Codex two-axis model: the same profile
// yields different interruption behavior under different approval modes.
func TestGoalApprovalModeAxis(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	// on_request (default): a risk-2 install requires approval.
	on, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(on.SessionID, ws, "safe-edit", "on_request", nil)
	dec, err := d.kern.Request(on.SessionID, "CommandExec", "npm install left-pad", "")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "requires_approval" {
		t.Fatalf("on_request: expected requires_approval, got %s", dec.Decision)
	}

	// never: the same command is auto-allowed (deny would still stand).
	nv, _ := d.store.CreateSessionMode(ws, "safe-edit", "never")
	d.kern.InitSessionFull(nv.SessionID, ws, "safe-edit", "never", nil)
	dec2, _ := d.kern.Request(nv.SessionID, "CommandExec", "npm install left-pad", "")
	if dec2.Decision != "allowed" {
		t.Fatalf("never: expected allowed, got %s", dec2.Decision)
	}
	// never must NOT rescue an outright deny.
	dec3, _ := d.kern.Request(nv.SessionID, "CommandExec", "rm -rf /", "")
	if dec3.Decision != "denied" {
		t.Fatalf("never must not allow rm -rf, got %s", dec3.Decision)
	}
}

// TestGoalApprovalMemory proves ApprovedForSession cuts repeat approvals.
func TestGoalApprovalMemory(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)

	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install a", "")
	if dec.Decision != "requires_approval" {
		t.Fatalf("expected approval prompt, got %s", dec.Decision)
	}
	// Approve for the whole session.
	if _, err := d.kern.ApproveForSession(sess.SessionID, dec.DecisionID, "user"); err != nil {
		t.Fatal(err)
	}
	// A later npm command (same capability + "npm" prefix) auto-satisfies.
	dec2, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install b", "")
	if dec2.Decision != "allowed" {
		t.Fatalf("cached approval should auto-allow, got %s (%s)", dec2.Decision, dec2.Reason)
	}
}

// TestGoalSuccessCriteriaVerified proves "done" is machine-checked: the model
// claims done while a required file is missing; the loop rejects it, the
// model then creates the file, and only then does the task complete.
func TestGoalSuccessCriteriaVerified(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"done","summary":"I think I'm done"}`, // premature — file missing
		`{"tool":"patch","path":"expected.txt","content":"created\n"}`,
		`{"tool":"done","summary":"now really done"}`,
	}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.SubmitWithGoal(sess.SessionID, sess.WorkspaceID, "create expected.txt",
		[]scheduler.SuccessCheck{{Kind: "file_exists", Path: "expected.txt"}})
	d.runTask(sess, task)

	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("task should complete after criteria pass, got %s", tk.Status)
	}
	if _, err := os.Stat(filepath.Join(ws, "expected.txt")); err != nil {
		t.Fatal("success criterion should have forced the file to be created")
	}
}

// TestGoalSuccessCriteriaRejectsFalseDone proves a task that never satisfies
// its criteria degrades rather than falsely completing.
func TestGoalSuccessCriteriaRejectsFalseDone(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	// Model always claims done but never creates the file.
	steps := make([]string, 20)
	for i := range steps {
		steps[i] = `{"tool":"done","summary":"done (lying)"}`
	}
	d.SetReasoner(&scriptedReasoner{steps: steps})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.SubmitWithGoal(sess.SessionID, sess.WorkspaceID, "create it",
		[]scheduler.SuccessCheck{{Kind: "file_exists", Path: "never.txt"}})
	d.runTask(sess, task)

	if tk, _ := d.sched.Get(task.TaskID); tk.Status == "completed" {
		t.Fatal("unverified done must NOT complete")
	}
}
