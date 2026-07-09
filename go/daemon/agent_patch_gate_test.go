package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAgentPatchRequiresApprovalUnderInteractiveMode reproduces the
// governance bypass: agentPatch (the agent's own internal write path, driven
// by d.runTask via the "patch" tool) called d.kern.PatchPropose/PatchApply
// directly with a fabricated approver="agent" and no gate check at all,
// completely bypassing the same requires_approval discipline that
// workspace.patch.apply enforces via checkPatchGate. Under a safe-edit
// profile with interactive approval on, PatchApply always evaluates to
// requires_approval (crates/carina-policy evaluate()) — so every
// agent-authored file write must pause for an operator, exactly like a
// gated command (agentRun/resolveApproval). Before the fix, the file lands
// on disk immediately, self-approved as approver="agent", with no
// permission.request ever published and the daemon's own patchGates map
// never recording the patch.
func TestAgentPatchRequiresApprovalUnderInteractiveMode(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 3 * time.Second
	reqs := permissionRequests(d)

	target := filepath.Join(ws, "hello.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, sess.PermissionProfile, nil); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"thought":"read it","action":{"tool":"read","path":"hello.txt"}}`,
		`{"thought":"edit","action":{"tool":"patch","path":"hello.txt","content":"hello\n// attacker/model-controlled content\n"}}`,
		`{"thought":"finish","action":{"tool":"done","summary":"edited hello.txt"}}`,
	}})
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit hello.txt")

	done := make(chan struct{})
	go func() {
		d.runTask(sess, task)
		close(done)
	}()

	// The gated write must pause for an operator, exactly like a gated
	// command — a permission.request must be published, and the file must
	// NOT be written before the operator resolves it.
	var decisionID string
	select {
	case decisionID = <-reqs:
	case <-time.After(2 * time.Second):
		t.Fatal("agentPatch never requested approval for a requires_approval PatchApply — the gate was bypassed")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("file was written before the operator approved the patch: %q", got)
	}

	// The TUI's approval RPC unblocks it, same as any other gated action.
	if _, err := d.handleApprove(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": decisionID, "approver": "operator",
	})); err != nil {
		t.Fatalf("handleApprove: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("approving the patch did not unblock the agent's run")
	}

	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n// attacker/model-controlled content\n" {
		t.Fatalf("approved patch was not applied: %q", got)
	}
}

// TestAgentPatchPermissionRequestCarriesDiff proves the operator-facing
// approval prompt for a PatchApply decision actually shows the reviewable
// artifact: permission.request must carry a "diff" field with the real
// unified diff the kernel is about to apply, not just the capability name
// and patch_id. Without this, go/tui's diff renderer (ColorDiff) and
// approval overlay (openApproval reading ev["diff"]) are unreachable with
// real data — an operator approving a patch could not see what content
// they're approving even in principle.
func TestAgentPatchPermissionRequestCarriesDiff(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 3 * time.Second

	type patchRequest struct {
		decisionID string
		diff       string
	}
	reqs := make(chan patchRequest, 4)
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "permission.request" && ev["capability"] == "PatchApply" {
			diff, _ := ev["diff"].(string)
			decisionID, _ := ev["decision_id"].(string)
			reqs <- patchRequest{decisionID: decisionID, diff: diff}
		}
	})

	target := filepath.Join(ws, "hello.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, sess.PermissionProfile, nil); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"thought":"read it","action":{"tool":"read","path":"hello.txt"}}`,
		`{"thought":"edit","action":{"tool":"patch","path":"hello.txt","content":"hello\nworld\n"}}`,
		`{"thought":"finish","action":{"tool":"done","summary":"edited hello.txt"}}`,
	}})
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit hello.txt")

	done := make(chan struct{})
	go func() {
		d.runTask(sess, task)
		close(done)
	}()

	var req patchRequest
	select {
	case req = <-reqs:
	case <-time.After(2 * time.Second):
		t.Fatal("no permission.request for the PatchApply decision was observed")
	}

	if req.diff == "" {
		t.Fatal("permission.request for a patch must carry a non-empty diff field")
	}
	if !strings.Contains(req.diff, "world") {
		t.Fatalf("diff must show the actual proposed content, got: %q", req.diff)
	}
	if !strings.Contains(req.diff, "hello.txt") {
		t.Fatalf("diff must reference the affected file, got: %q", req.diff)
	}

	// Deny it so the run resolves immediately instead of idling out the
	// approval window.
	if _, err := d.handleDeny(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": req.decisionID, "approver": "operator",
	})); err != nil {
		t.Fatalf("handleDeny: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("denying the patch did not unblock the agent's run")
	}
}

// TestAgentPatchDeniedNotWritten proves the mirror outcome: a denied patch
// must never touch the workspace.
func TestAgentPatchDeniedNotWritten(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 3 * time.Second
	reqs := permissionRequests(d)

	target := filepath.Join(ws, "hello.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, sess.PermissionProfile, nil); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"thought":"read it","action":{"tool":"read","path":"hello.txt"}}`,
		`{"thought":"edit","action":{"tool":"patch","path":"hello.txt","content":"hello\n// should never land\n"}}`,
		`{"thought":"finish","action":{"tool":"done","summary":"edited hello.txt"}}`,
	}})
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit hello.txt")

	done := make(chan struct{})
	go func() {
		d.runTask(sess, task)
		close(done)
	}()

	var decisionID string
	select {
	case decisionID = <-reqs:
	case <-time.After(2 * time.Second):
		t.Fatal("agentPatch never requested approval for a requires_approval PatchApply")
	}

	if _, err := d.handleDeny(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": decisionID,
		"approver": "operator", "reason": "denied by operator in carina-tui",
	})); err != nil {
		t.Fatalf("handleDeny: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("denying the patch did not unblock the agent's run")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("denied patch must not touch the workspace: %q", got)
	}
}
