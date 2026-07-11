package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/kernel"
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
	if _, err := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, "late-operator", ""); err == nil {
		t.Fatal("timed-out kernel decision remained approvable")
	}
}

func TestInteractivePatchApprovalTimeoutClosesPatchGate(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 30 * time.Millisecond
	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "patch")
	patchID, decisionID, decision := proposeGovernedPatch(t, d, sess.SessionID, "timeout.txt", "after\n")
	if decision != "requires_approval" {
		t.Fatalf("decision = %s", decision)
	}
	dec, err := d.kernDecisionForPatch(sess.SessionID, patchID, decisionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.resolveApproval(sess, task, dec, "apply patch"); ok {
		t.Fatal("timed-out patch approval was allowed")
	}
	d.mu.Lock()
	status := d.patchGates[patchID].status
	d.mu.Unlock()
	if status != "expired" {
		t.Fatalf("patch gate status = %s, want expired", status)
	}
	if _, err := d.handleApprove(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "decision_id": decisionID,
	})); err == nil || !strings.Contains(err.Error(), "approval_expired") {
		t.Fatalf("late patch approval error = %v", err)
	}
}

func (d *Daemon) kernDecisionForPatch(sessionID, patchID, decisionID string) (*kernel.Decision, error) {
	// The decision is already in the kernel pending map. Reconstruct the public
	// envelope used by resolveApproval without minting a second decision.
	return &kernel.Decision{
		DecisionID: decisionID, Decision: "requires_approval",
		Capability: "PatchApply", Resource: patchID,
	}, nil
}

func TestApprovalResolveAcceptsCanonicalAndLegacyBooleans(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
	}{
		{name: "canonical", params: map[string]any{"approve": true}},
		{name: "legacy", params: map[string]any{"allow": true}},
		{name: "matching aliases", params: map[string]any{"approve": true, "allow": true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, ws := newLoopDaemon(t)
			defer d.Close()
			d.SetInteractiveApproval(true)
			sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
			d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
			task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
			dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)
			reqs := permissionRequests(d)
			result := make(chan bool, 1)
			go func() { _, ok := d.resolveApproval(sess, task, dec, "install"); result <- ok }()
			select {
			case <-reqs:
			case <-time.After(2 * time.Second):
				t.Fatal("permission request not published")
			}
			params := map[string]any{"decision_id": dec.DecisionID}
			for key, value := range test.params {
				params[key] = value
			}
			if _, err := d.handleApprovalResolve(mustJSON(t, params)); err != nil {
				t.Fatal(err)
			}
			if !<-result {
				t.Fatal("approval was not granted")
			}
		})
	}
}

func TestApprovalResolveRejectsMissingOrConflictingBoolean(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)
	reqs := permissionRequests(d)
	result := make(chan bool, 1)
	go func() { _, ok := d.resolveApproval(sess, task, dec, "install"); result <- ok }()
	select {
	case <-reqs:
	case <-time.After(2 * time.Second):
		t.Fatal("permission request not published")
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{"decision_id": dec.DecisionID})); err == nil {
		t.Fatal("missing approve/allow was accepted")
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{
		"decision_id": dec.DecisionID, "approve": true, "allow": false,
	})); err == nil {
		t.Fatal("conflicting approve/allow was accepted")
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{
		"decision_id": dec.DecisionID, "approve": false,
	})); err != nil {
		t.Fatal(err)
	}
	if <-result {
		t.Fatal("cleanup denial unexpectedly granted approval")
	}
	if _, err := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, "late", ""); err == nil {
		t.Fatal("denied decision remained pending")
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

func TestRiskReviewAdvisoryRecordsAndAllows(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "install dependency")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)
	if dec.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %s", dec.Decision)
	}

	if _, ok := d.resolveApproval(sess, task, dec, "npm install left-pad"); !ok {
		t.Fatal("advisory risk review must not block autonomous approval")
	}
	payload := lastRiskReviewPayload(t, d, sess.SessionID)
	if payload["mode"] != "advisory" || payload["outcome"] != "allow" || payload["source"] != "heuristic" {
		t.Fatalf("unexpected advisory review: %+v", payload)
	}
}

func TestRiskReviewEnforceBlocksHighRiskApproval(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := d.SetRiskReviewMode("enforce"); err != nil {
		t.Fatal(err)
	}

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "move file")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "mv a b", task.TaskID)
	if dec.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %s", dec.Decision)
	}

	if _, ok := d.resolveApproval(sess, task, dec, "mv a b"); ok {
		t.Fatal("enforce mode must block high-risk autonomous approval")
	}
	payload := lastRiskReviewPayload(t, d, sess.SessionID)
	if payload["mode"] != "enforce" || payload["outcome"] != "deny" || payload["risk"] != "high" {
		t.Fatalf("unexpected enforce review: %+v", payload)
	}
}

func TestRiskReviewModelDenyEnforce(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := d.SetRiskReviewMode("enforce"); err != nil {
		t.Fatal(err)
	}
	d.SetRiskReviewer(&scriptedReasoner{steps: []string{
		`{"outcome":"deny","risk":"high","authorization":"low","rationale":"task did not justify dependency change"}`,
	}})

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "install dependency")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)
	if dec.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %s", dec.Decision)
	}

	if _, ok := d.resolveApproval(sess, task, dec, "npm install left-pad"); ok {
		t.Fatal("model deny must block in enforce mode")
	}
	payload := lastRiskReviewPayload(t, d, sess.SessionID)
	if payload["source"] != "model" || payload["outcome"] != "deny" {
		t.Fatalf("unexpected model review: %+v", payload)
	}
}

func TestInteractiveApprovalBypassesRiskReview(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.SetRiskReviewer(&scriptedReasoner{steps: []string{
		`{"outcome":"deny","risk":"high","authorization":"low","rationale":"would block if autonomous"}`,
	}})
	reqs := permissionRequests(d)

	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "install dependency")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install left-pad", task.TaskID)
	out := make(chan bool, 1)
	go func() {
		_, ok := d.resolveApproval(sess, task, dec, "npm install left-pad")
		out <- ok
	}()
	<-reqs
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{
		"decision_id": dec.DecisionID, "approve": true})); err != nil {
		t.Fatal(err)
	}
	if ok := <-out; !ok {
		t.Fatal("operator approval should still allow")
	}
	if payload := lastRiskReviewPayloadOrNil(t, d, sess.SessionID); payload != nil {
		t.Fatalf("interactive approval must not run autonomous risk review: %+v", payload)
	}
}

func TestParseRiskReviewAssessment(t *testing.T) {
	got, err := parseRiskReviewAssessment("```json\n{\"outcome\":\"ALLOW\",\"risk\":\"LOW\",\"authorization\":\"HIGH\",\"rationale\":\"ok\"}\n```")
	if err != nil {
		t.Fatalf("fenced assessment should parse: %v", err)
	}
	if got.Outcome != "allow" || got.Risk != "low" || got.Authorization != "high" || got.Rationale != "ok" {
		t.Fatalf("unexpected assessment: %+v", got)
	}
	if _, err := parseRiskReviewAssessment(`{"outcome":"maybe","risk":"low","authorization":"high"}`); err == nil {
		t.Fatal("invalid outcome must fail")
	}
}

func lastRiskReviewPayload(t *testing.T, d *Daemon, sessionID string) map[string]any {
	t.Helper()
	payload := lastRiskReviewPayloadOrNil(t, d, sessionID)
	if payload == nil {
		t.Fatal("missing risk_review audit event")
	}
	return payload
}

func lastRiskReviewPayloadOrNil(t *testing.T, d *Daemon, sessionID string) map[string]any {
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
		if ev.Type == "TaskCreated" && ev.Payload["status"] == "risk_review" {
			payload = ev.Payload
		}
	}
	return payload
}
