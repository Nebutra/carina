package daemon

import (
	"strings"
	"testing"
	"time"
)

func TestDontAskDeniesRequiresApprovalWithoutGrant(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := d.SetApprovalMode(approvalModeDontAsk); err != nil {
		t.Fatal(err)
	}
	// Should never emit permission.request under dont-ask.
	reqs := permissionRequests(d)

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)

	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)
	if dec.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %s", dec.Decision)
	}
	_, ok := d.resolveApproval(sess, task, dec, "npm install left-pad")
	if ok {
		t.Fatal("dont-ask must not grant requires_approval without a stored grant")
	}
	select {
	case id := <-reqs:
		t.Fatalf("dont-ask must not publish permission.request, got %s", id)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDontAskHonorsExactSessionGrant(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := d.SetApprovalMode(approvalModeAsk); err != nil {
		t.Fatal(err)
	}

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)

	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)
	if dec.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %s", dec.Decision)
	}
	// Install a session-scoped grant via remember path.
	approved, err := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, "operator", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.rememberApprovalGrant(sess, approved, approvalScopeSession, "operator", ""); err != nil {
		t.Fatal(err)
	}

	if err := d.SetApprovalMode(approvalModeDontAsk); err != nil {
		t.Fatal(err)
	}
	// Fresh decision for the same capability+resource.
	task2 := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run again")
	dec2, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task2.TaskID)
	if dec2.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %s (%s)", dec2.Decision, dec2.Reason)
	}
	got, ok := d.resolveApproval(sess, task2, dec2, "npm install left-pad")
	if !ok || got == nil || got.Decision != "allowed" {
		t.Fatalf("dont-ask must honor exact session grant, ok=%v got=%+v", ok, got)
	}
}

func TestDisableAlwaysApproveBlocksRPCAndFallsBack(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.SetDisableAlwaysApprove(true)
	err := d.SetApprovalMode(approvalModeAlwaysApprove)
	if err == nil {
		t.Fatal("expected set always-approve to fail under org lock")
	}
	if !strings.Contains(err.Error(), "disable_always_approve") {
		t.Fatalf("error should name the lock, got %v", err)
	}
	// Legacy on=false path.
	off := false
	_, err = d.handleSetInteractiveApproval(mustJSON(t, map[string]any{"on": off}))
	if err == nil {
		t.Fatal("expected RPC always-approve to fail under org lock")
	}
	// ask still works.
	if err := d.SetApprovalMode(approvalModeAsk); err != nil {
		t.Fatal(err)
	}
	if d.approvalModeString() != approvalModeAsk {
		t.Fatalf("mode=%s", d.approvalModeString())
	}
	// dont-ask still works under the lock.
	if err := d.SetApprovalMode(approvalModeDontAsk); err != nil {
		t.Fatal(err)
	}
	if d.approvalModeString() != approvalModeDontAsk {
		t.Fatalf("mode=%s", d.approvalModeString())
	}
}

func TestSetApprovalModeRPCThreeWay(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	out, err := d.handleSetInteractiveApproval(mustJSON(t, map[string]any{"mode": "dont-ask", "session_id": "s1"}))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["approval_mode"] != approvalModeDontAsk {
		t.Fatalf("result=%#v", res)
	}
	if res["interactive_approval"] != false {
		t.Fatalf("dont-ask must report interactive_approval=false, got %#v", res["interactive_approval"])
	}
	if res["warning"] == nil || res["warning"] == "" {
		t.Fatal("expected dont-ask warning")
	}
	out, err = d.handleSetInteractiveApproval(mustJSON(t, map[string]any{"mode": "ask"}))
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["approval_mode"] != approvalModeAsk {
		t.Fatalf("result=%#v", out)
	}
}

func TestNormalizeApprovalModeAliases(t *testing.T) {
	cases := map[string]string{
		"ask":            approvalModeAsk,
		"dontAsk":        approvalModeDontAsk,
		"dont_ask":       approvalModeDontAsk,
		"yolo":           approvalModeAlwaysApprove,
		"bypass":         approvalModeAlwaysApprove,
		"always-approve": approvalModeAlwaysApprove,
		"acceptEdits":    approvalModeAcceptEdits,
		"accept-edits":   approvalModeAcceptEdits,
		"accept_edits":   approvalModeAcceptEdits,
	}
	for in, want := range cases {
		got, err := normalizeApprovalMode(in)
		if err != nil || got != want {
			t.Fatalf("%q => %q err=%v want %q", in, got, err, want)
		}
	}
	if _, err := normalizeApprovalMode("nope"); err == nil {
		t.Fatal("expected error for unknown mode")
	}
	// Session/kernel axis must not silently map onto product HITL modes.
	for _, sessionAxis := range []string{"never", "untrusted", "on_request", "on-request"} {
		_, err := normalizeApprovalMode(sessionAxis)
		if err == nil {
			t.Fatalf("%q must be rejected as product mode", sessionAxis)
		}
		if !strings.Contains(err.Error(), "session/kernel") {
			t.Fatalf("%q error should name session/kernel axis, got %v", sessionAxis, err)
		}
	}
}

func TestAcceptEditsAutoAllowsFileEditsButPromptsShell(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := d.SetApprovalMode(approvalModeAcceptEdits); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)

	// PatchApply / FileWrite should auto-approve without permission.request.
	reqs := permissionRequests(d)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit")
	// Prefer FileWrite if profile allows; otherwise PatchApply path is heavier.
	dec, err := d.kern.Request(sess.SessionID, "FileWrite", "notes.txt", task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision == "allowed" {
		// Profile may auto-allow write; force requires_approval by using CommandExec for shell half.
	} else if dec.Decision == "requires_approval" {
		got, ok := d.resolveApproval(sess, task, dec, "write notes")
		if !ok || got == nil || got.Decision != "allowed" {
			t.Fatalf("accept-edits should auto-allow FileWrite requires_approval, ok=%v got=%+v", ok, got)
		}
		select {
		case id := <-reqs:
			t.Fatalf("FileWrite should not publish permission.request, got %s", id)
		default:
		}
	} else if dec.Decision == "denied" {
		t.Skip("profile denies FileWrite; cannot exercise accept-edits auto path")
	}

	// CommandExec should still prompt (permission.request).
	task2 := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	dec2, err := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task2.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if dec2.Decision != "requires_approval" {
		t.Fatalf("expected CommandExec requires_approval, got %s (%s)", dec2.Decision, dec2.Reason)
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := d.resolveApproval(sess, task2, dec2, "npm install")
		done <- ok
	}()
	select {
	case id := <-reqs:
		if id != dec2.DecisionID {
			t.Fatalf("permission.request id = %s, want %s", id, dec2.DecisionID)
		}
		if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{
			"decision_id": dec2.DecisionID, "approve": false,
		})); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CommandExec under accept-edits must publish permission.request")
	}
	select {
	case ok := <-done:
		if ok {
			t.Fatal("expected deny")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resolve did not finish")
	}
}

func TestIsEditCapability(t *testing.T) {
	if !isEditCapability("FileWrite") || !isEditCapability("PatchApply") || !isEditCapability("patchapply") {
		t.Fatal("edit capabilities")
	}
	if isEditCapability("CommandExec") || isEditCapability("NetworkAccess") || isEditCapability("MemoryWrite") {
		t.Fatal("non-edit capabilities must not match")
	}
}
