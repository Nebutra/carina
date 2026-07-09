package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTUIApprovalRPCUnblocksInteractiveWait reproduces the round-trip gap
// between the daemon's interactive-approval wait (awaitInteractiveApproval,
// which blocks on d.pendingApprovals fed by handleApprovalResolve /
// task.approval.resolve) and the TUI's approval overlay, which resolves a
// permission.request over task.action.approve / task.action.deny
// (handleApprove / handleDeny) — the exact RPC methods go/tui/approval.go
// calls. Before the fix, a TUI approve is recorded by the kernel as allowed
// while the gated action itself times out and is denied: audit says
// allowed, runtime denied it. This test spawns the real Rust kernel
// subprocess (via newLoopDaemon) and drives a real agent task through
// d.runTask so the wait is the genuine awaitInteractiveApproval pause, then
// resolves it with the same handlers the TUI's RPC calls dispatch to,
// proving BOTH outcomes actually unblock the run with a matching result.
func TestTUIApprovalRPCUnblocksInteractiveWait(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 30 * time.Second // must NOT be hit if the round trip works
	reqs := permissionRequests(d)

	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// --- APPROVE path: task.action.approve (handleApprove), exactly what
	// go/tui/approval.go resolveApproval calls on "y".
	//
	// The gated command is a local `mv` (risk level 3 per classify_atom,
	// same "mutation" bucket the tui-bubbletea/tui-ratatui spikes drove for
	// their G2 gate) rather than a real package-manager install: it needs
	// no network round trip, so the round-trip timing this test asserts on
	// (both `done` channels within 20s) measures only the approval-signaling
	// path, not registry/network reachability.
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, sess.PermissionProfile, nil); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"thought":"rename","action":{"tool":"run","command":["mv","hello.txt","renamed-approve.txt"]}}`,
		`{"thought":"finish","action":{"tool":"done","summary":"renamed hello.txt"}}`,
	}})
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "rename hello.txt")

	done := make(chan struct{})
	go func() {
		d.runTask(sess, task)
		close(done)
	}()

	var decisionID string
	select {
	case decisionID = <-reqs:
	case <-time.After(20 * time.Second):
		t.Fatal("no permission.request emitted for the gated command")
	}
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "waiting_approval" {
		t.Fatalf("task should pause at waiting_approval, got %s", tk.Status)
	}

	// The TUI approve path: task.action.approve -> handleApprove.
	if _, err := d.handleApprove(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": decisionID, "approver": "operator",
	})); err != nil {
		t.Fatalf("handleApprove: %v", err)
	}

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("approving via task.action.approve did not unblock the daemon's interactive wait — audit vs runtime mismatch")
	}
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("approved task must complete, got status %s", tk.Status)
	}
	assertDecisionAudited(t, d, sess.SessionID, decisionID, "allowed")

	// --- DENY path: task.action.deny (handleDeny), exactly what
	// go/tui/approval.go resolveApproval calls on "n".
	if err := os.WriteFile(filepath.Join(ws, "world.txt"), []byte("world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess2, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess2.SessionID, ws, sess2.PermissionProfile, nil); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"thought":"rename","action":{"tool":"run","command":["mv","world.txt","renamed-deny.txt"]}}`,
		`{"thought":"finish","action":{"tool":"done","summary":"renamed world.txt"}}`,
	}})
	task2 := d.sched.Submit(sess2.SessionID, sess2.WorkspaceID, "rename world.txt")

	done2 := make(chan struct{})
	go func() {
		d.runTask(sess2, task2)
		close(done2)
	}()

	var decisionID2 string
	select {
	case decisionID2 = <-reqs:
	case <-time.After(20 * time.Second):
		t.Fatal("no permission.request emitted for the gated command (deny path)")
	}

	// The TUI deny path: task.action.deny -> handleDeny.
	if _, err := d.handleDeny(mustJSON(t, map[string]any{
		"session_id": sess2.SessionID, "decision_id": decisionID2,
		"approver": "operator", "reason": "denied by operator in carina-tui",
	})); err != nil {
		t.Fatalf("handleDeny: %v", err)
	}

	select {
	case <-done2:
	case <-time.After(20 * time.Second):
		t.Fatal("denying via task.action.deny did not unblock the daemon's interactive wait — the run hung to the timeout instead of resolving immediately")
	}
	assertDecisionAudited(t, d, sess2.SessionID, decisionID2, "denied")
}

// assertDecisionAudited confirms the kernel-recorded verdict for decisionID
// matches want, and that the daemon's own approval_resolved bookkeeping
// event agrees — audit and runtime must never disagree.
func assertDecisionAudited(t *testing.T, d *Daemon, sessionID, decisionID, want string) {
	t.Helper()
	raw, err := d.kern.ReadEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var events []struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	grantedWant := want == "allowed"
	var sawResolved bool
	for _, ev := range events {
		if ev.Type == "TaskCreated" && ev.Payload["status"] == "approval_resolved" && ev.Payload["decision_id"] == decisionID {
			sawResolved = true
			if granted, _ := ev.Payload["granted"].(bool); granted != grantedWant {
				t.Fatalf("approval_resolved granted=%v, want %v (decision %s)", granted, grantedWant, decisionID)
			}
		}
	}
	if !sawResolved {
		t.Fatalf("no approval_resolved audit event for decision %s", decisionID)
	}
}
