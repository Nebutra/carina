package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// proposeGovernedPatch proposes one full-file change through the RPC handler
// and returns the patch id plus the PatchApply gate decision minted with it.
func proposeGovernedPatch(t *testing.T, d *Daemon, sessionID, path, content string) (patchID, decisionID, verdict string) {
	t.Helper()
	res, err := d.handlePatchPropose(mustJSON(t, map[string]any{
		"session_id": sessionID,
		"reason":     "governed edit",
		"files":      []map[string]any{{"path": path, "new_content": content}},
	}))
	if err != nil {
		t.Fatalf("patch propose: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var patch struct {
		PatchID       string `json:"patch_id"`
		ApplyDecision struct {
			DecisionID string `json:"decision_id"`
			Decision   string `json:"decision"`
		} `json:"apply_decision"`
	}
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	if patch.PatchID == "" {
		t.Fatal("propose returned no patch_id")
	}
	return patch.PatchID, patch.ApplyDecision.DecisionID, patch.ApplyDecision.Decision
}

// lastPatchRefusal returns the payload of the most recent PolicyViolation
// audit event recorded for a refused patch apply (nil if none).
func lastPatchRefusal(t *testing.T, d *Daemon, sessionID, patchID string) map[string]any {
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
	var payload map[string]any
	for _, ev := range events {
		if ev.Type == "PolicyViolation" && ev.Payload["patch_id"] == patchID {
			payload = ev.Payload
		}
	}
	return payload
}

// TestPatchApplyWithoutApprovalRefused reproduces the governance bypass found
// by the TUI spikes (docs/plans/tui-stack-decision.md, spike verdict):
// workspace.patch.apply must not apply a patch whose PatchApply decision is
// requires_approval and has not been approved. The refusal must be observable
// (error + PolicyViolation audit event), and an approved decision must
// unblock the same apply.
func TestPatchApplyWithoutApprovalRefused(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}

	patchID, decisionID, verdict := proposeGovernedPatch(t, d, sess.SessionID, "a.txt", "governed\n")
	if verdict != "requires_approval" {
		t.Fatalf("safe-edit must gate PatchApply as requires_approval, got %q", verdict)
	}
	if decisionID == "" {
		t.Fatal("propose must return the decision_id that gates the apply")
	}

	// Bypass attempt: apply without approving the decision.
	if _, err := d.handlePatchApply(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "patch_id": patchID,
	})); err == nil {
		t.Fatal("apply without an approved decision must refuse")
	} else if !strings.Contains(err.Error(), "approval_required") {
		t.Fatalf("refusal must carry the approval_required code, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("refused patch must not touch the workspace")
	}
	payload := lastPatchRefusal(t, d, sess.SessionID, patchID)
	if payload == nil {
		t.Fatal("refused apply must record a PolicyViolation audit event")
	}
	if payload["refusal"] != "approval_required" || payload["capability"] != "PatchApply" {
		t.Fatalf("unexpected refusal audit payload: %+v", payload)
	}

	// The approved decision unblocks the same apply.
	if _, err := d.handleApprove(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": decisionID,
	})); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := d.handlePatchApply(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "patch_id": patchID,
	})); err != nil {
		t.Fatalf("apply after approval must proceed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(ws, "a.txt"))
	if err != nil || string(got) != "governed\n" {
		t.Fatalf("approved patch must land on disk: %q, %v", got, err)
	}
}

// TestPatchApplyDeniedDecisionRefused: a denied decision refuses the apply
// permanently, with the refusal audited.
func TestPatchApplyDeniedDecisionRefused(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}

	patchID, decisionID, _ := proposeGovernedPatch(t, d, sess.SessionID, "b.txt", "denied\n")
	if _, err := d.handleDeny(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": decisionID, "reason": "operator refused",
	})); err != nil {
		t.Fatalf("deny: %v", err)
	}

	if _, err := d.handlePatchApply(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "patch_id": patchID,
	})); err == nil {
		t.Fatal("apply with a denied decision must refuse")
	} else if !strings.Contains(err.Error(), "approval_denied") {
		t.Fatalf("refusal must carry the approval_denied code, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("denied patch must not touch the workspace")
	}
	payload := lastPatchRefusal(t, d, sess.SessionID, patchID)
	if payload == nil || payload["refusal"] != "approval_denied" {
		t.Fatalf("denied apply must record a PolicyViolation audit event: %+v", payload)
	}
}

// TestPatchApplyExpiredDecisionRefused: an unresolved decision older than the
// approval window expires; the apply refuses and the expiry is audited.
func TestPatchApplyExpiredDecisionRefused(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.approvalTimeout = 30 * time.Millisecond

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}

	patchID, decisionID, _ := proposeGovernedPatch(t, d, sess.SessionID, "c.txt", "expired\n")
	time.Sleep(60 * time.Millisecond)

	if _, err := d.handlePatchApply(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "patch_id": patchID,
	})); err == nil {
		t.Fatal("apply with an expired decision must refuse")
	} else if !strings.Contains(err.Error(), "approval_expired") {
		t.Fatalf("refusal must carry the approval_expired code, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("expired patch must not touch the workspace")
	}
	payload := lastPatchRefusal(t, d, sess.SessionID, patchID)
	if payload == nil || payload["refusal"] != "approval_expired" {
		t.Fatalf("expired apply must record a PolicyViolation audit event: %+v", payload)
	}

	// The expired decision can no longer be approved.
	if _, err := d.handleApprove(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": decisionID,
	})); err == nil {
		t.Fatal("approving an expired decision must fail")
	}
}

// TestLateApproveWithoutPriorApplyStillExpires reproduces the race in
// handleApprove: checkPatchGate only discovers an elapsed approval window
// when workspace.patch.apply is actually called (it lazily flips the gate
// to "expired" as a side effect of being checked). If instead
// task.action.approve (handleApprove) is called first — after the window
// has already elapsed, but before any apply attempt ever ran checkPatchGate
// — handleApprove unconditionally flips a "requires_approval" gate straight
// to "allowed" with no expiry check of its own, so a stale, late approval
// silently succeeds. The fix must make handleApprove refuse (or expire) a
// gate whose window has already elapsed, matching checkPatchGate's policy,
// regardless of call order.
func TestLateApproveWithoutPriorApplyStillExpires(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.approvalTimeout = 30 * time.Millisecond

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}

	patchID, decisionID, verdict := proposeGovernedPatch(t, d, sess.SessionID, "d.txt", "late\n")
	if verdict != "requires_approval" {
		t.Fatalf("expected requires_approval, got %q", verdict)
	}

	// The approval window elapses with no apply attempt in between — nothing
	// has run checkPatchGate yet, so the gate map still says
	// "requires_approval" verbatim.
	time.Sleep(60 * time.Millisecond)

	// An operator's approval (or the TUI's task.action.approve) arrives late,
	// after the window already elapsed.
	if _, err := d.handleApprove(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": decisionID, "approver": "operator",
	})); err == nil {
		t.Fatal("approving after the window elapsed must fail, not silently succeed")
	}

	// The apply must still refuse — the late approval must not have unlocked it.
	if _, err := d.handlePatchApply(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "patch_id": patchID,
	})); err == nil {
		t.Fatal("apply must refuse: the approval window had already elapsed before it was approved")
	} else if !strings.Contains(err.Error(), "approval_expired") {
		t.Fatalf("refusal must carry the approval_expired code, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "d.txt")); !os.IsNotExist(err) {
		t.Fatal("a patch approved past its window must not touch the workspace")
	}
}

// TestExpiryDenyFailureIsAudited reproduces the scenario expirePatchGateStatus
// does not fully close: handleApprove checks expirePatchGateIfStale, finds
// the window not yet elapsed, and proceeds to the (real, round-trip)
// d.kern.ApproveWithRole call — which resolves the kernel decision — before
// it ever gets to flip the daemon-side gate to "allowed" (that flip is the
// last step in handleApprove, after the kernel call returns). If the
// approval window elapses during that in-flight kernel call, a concurrent
// apply attempt's checkPatchGate -> expirePatchGateStatus still finds the
// daemon-side gate saying "requires_approval" (the flip to "allowed"
// hasn't happened yet) and tries to expire-and-deny a decision the kernel
// has already resolved as allowed. That Deny call fails — and
// expirePatchGateStatus discards the failure outright
// ("_, _ = d.kern.Deny(...)"), leaving no trace anywhere: not in the
// daemon's return value (checkPatchGate still reports "expired" from the
// gate's own daemon-side status regardless), not in the audit log. The fix
// must make that failure observable, matching how every other patch-gate
// refusal in this file is audited via recordPatchRefusal.
func TestExpiryDenyFailureIsAudited(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.approvalTimeout = 30 * time.Millisecond

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}

	patchID, decisionID, verdict := proposeGovernedPatch(t, d, sess.SessionID, "race.txt", "race\n")
	if verdict != "requires_approval" {
		t.Fatalf("expected requires_approval, got %q", verdict)
	}

	// Simulate the kernel resolving the decision as allowed, mid-flight,
	// exactly as handleApprove's own d.kern.ApproveWithRole call would —
	// but before the daemon-side gate bookkeeping (handleApprove's final
	// step) has run.
	if _, err := d.kern.ApproveWithRole(sess.SessionID, decisionID, "operator", ""); err != nil {
		t.Fatalf("kern.ApproveWithRole: %v", err)
	}

	time.Sleep(60 * time.Millisecond) // window elapses; gate still says requires_approval

	// A concurrent apply attempt's checkPatchGate discovers the stale gate
	// and tries to expire it — attesting the expiry means denying a
	// decision the kernel already resolved as allowed underneath it.
	d.expirePatchGateStatus(sess.SessionID, patchID)

	raw, err := d.kern.ReadEvents(sess.SessionID)
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
	var failureAudited bool
	for _, ev := range events {
		if ev.Type == "PolicyViolation" && ev.Payload["patch_id"] == patchID && ev.Payload["refusal"] == "expiry_deny_failed" {
			failureAudited = true
		}
	}
	if !failureAudited {
		t.Fatalf("expirePatchGateStatus's failed kernel Deny on already-resolved decision %s must be audited, not silently discarded; events: %+v", decisionID, events)
	}
}

// TestPatchGateEntriesArePrunedAfterRetention: patchGates/patchGateByDecision
// are written on every proposal (registerPatchGate) and never deleted, so a
// long-running daemon (or an autonomous agent proposing many small edits)
// accumulates two map entries per patch forever. A resolved (terminal) gate
// older than the retention window must be swept the next time a new gate is
// registered, so daemon memory is bounded without changing the observable
// behavior of any gate still within its retention window.
func TestPatchGateEntriesArePrunedAfterRetention(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.patchGateRetention = 20 * time.Millisecond

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}

	oldPatchID, oldDecisionID, _ := proposeGovernedPatch(t, d, sess.SessionID, "old.txt", "old\n")
	if _, err := d.handleDeny(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": oldDecisionID, "approver": "operator",
	})); err != nil {
		t.Fatalf("deny: %v", err)
	}

	time.Sleep(30 * time.Millisecond) // older than retention and terminal (denied)

	// Registering a fresh gate is the only place a sweep can piggyback
	// without a background goroutine; it must prune the aged, terminal
	// entry above but must not touch itself.
	newPatchID, newDecisionID, _ := proposeGovernedPatch(t, d, sess.SessionID, "new.txt", "new\n")

	d.mu.Lock()
	_, oldStillPresent := d.patchGates[oldPatchID]
	_, oldDecisionStillPresent := d.patchGateByDecision[oldDecisionID]
	_, newPresent := d.patchGates[newPatchID]
	_, newDecisionPresent := d.patchGateByDecision[newDecisionID]
	d.mu.Unlock()

	if oldStillPresent {
		t.Errorf("patch gate %s (denied, past retention) should have been pruned on the next registration", oldPatchID)
	}
	if oldDecisionStillPresent {
		t.Errorf("patchGateByDecision entry for %s should have been pruned alongside its gate", oldDecisionID)
	}
	if !newPresent {
		t.Errorf("the just-registered gate %s must survive its own registration's sweep", newPatchID)
	}
	if !newDecisionPresent {
		t.Errorf("the just-registered decision %s must survive its own registration's sweep", newDecisionID)
	}
}
