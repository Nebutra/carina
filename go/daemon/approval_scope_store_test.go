package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func TestApprovalGrantStoreScopesAndPersistence(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	store := newApprovalGrantStore(stateDir)
	sessA := &sessionstore.Session{SessionID: "sess_a", WorkspaceRoot: workspace}
	sessB := &sessionstore.Session{SessionID: "sess_b", WorkspaceRoot: workspace}

	sessionGrant := approvalGrant{
		Scope: approvalScopeSession, SessionID: sessA.SessionID, WorkspaceRoot: workspace,
		Capability: " CommandExec ", Resource: "npm install left-pad ", SourceDecisionID: "dec_session",
		Approver: "alice", CreatedAt: time.Now().UTC(),
	}
	if err := store.add(sessionGrant, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.match(sessA, "commandexec", "npm install left-pad"); !ok {
		t.Fatal("session grant did not match its exact session/capability/resource")
	}
	if _, ok := store.match(sessB, "CommandExec", "npm install left-pad"); ok {
		t.Fatal("session grant leaked into another session")
	}
	if _, ok := store.match(sessA, "CommandExec", "npm install right-pad"); ok {
		t.Fatal("session grant matched a different resource")
	}

	projectGrant := approvalGrant{
		Scope: approvalScopeProject, SessionID: sessA.SessionID, WorkspaceRoot: workspace,
		Capability: "PluginLoad", Resource: "mcp:github/create_issue", SourceDecisionID: "dec_project",
		Approver: "alice", CreatedAt: time.Now().UTC(),
	}
	if err := store.add(projectGrant, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.match(sessB, "PluginLoad", "mcp:github/create_issue"); !ok {
		t.Fatal("project grant did not cross sessions in the same workspace")
	}
	other := &sessionstore.Session{SessionID: "sess_c", WorkspaceRoot: t.TempDir()}
	if _, ok := store.match(other, "PluginLoad", "mcp:github/create_issue"); ok {
		t.Fatal("project grant leaked into another workspace")
	}

	reopened := newApprovalGrantStore(stateDir)
	if _, ok := reopened.match(sessB, "PluginLoad", "mcp:github/create_issue"); !ok {
		t.Fatal("project grant did not survive store reopen")
	}
	info, err := os.Stat(filepath.Join(stateDir, "approval-grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("grant store mode = %o, want 600", info.Mode().Perm())
	}
}

func TestApprovalGrantStoreFailsClosedWhenAuditAuthorizationFails(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	store := newApprovalGrantStore(stateDir)
	sess := &sessionstore.Session{SessionID: "sess_a", WorkspaceRoot: workspace}
	grant := approvalGrant{
		Scope: approvalScopeProject, WorkspaceRoot: workspace, Capability: "CommandExec",
		Resource: "npm install left-pad", SourceDecisionID: "dec_1", Approver: "alice", CreatedAt: time.Now().UTC(),
	}
	if err := store.add(grant, func() error { return errors.New("audit unavailable") }); err == nil {
		t.Fatal("grant creation succeeded without its authorization audit")
	}
	if _, ok := store.match(sess, "CommandExec", "npm install left-pad"); ok {
		t.Fatal("unaudited grant became visible")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "approval-grants.json")); !os.IsNotExist(err) {
		t.Fatalf("unaudited grant became durable: %v", err)
	}
}

func TestScopedApprovalRoundTripAndKernelVeto(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()

	sessA, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sessA.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}

	// Session scope: exact match in the same session only.
	dec, err := d.kern.Request(sessA.SessionID, "CommandExec", "npm install left-pad", "")
	if err != nil || dec.Decision != "requires_approval" {
		t.Fatalf("initial decision = %+v, err=%v", dec, err)
	}
	out, err := d.handleApprove(mustJSON(t, map[string]any{
		"session_id": sessA.SessionID, "decision_id": dec.DecisionID, "approver": "operator", "scope": "session",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["scope"]; got != approvalScopeSession {
		t.Fatalf("approved scope = %v, want session", got)
	}
	repeat, _ := d.kern.Request(sessA.SessionID, "CommandExec", "npm install left-pad", "")
	if approved, ok := d.approveFromStoredGrant(sessA, repeat); !ok || approved.Decision != "allowed" {
		t.Fatalf("session grant did not resolve exact repeat: approved=%+v ok=%v", approved, ok)
	}

	sessB, err := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sessB.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	repeatOtherSession, _ := d.kern.Request(sessB.SessionID, "CommandExec", "npm install left-pad", "")
	if _, ok := d.approveFromStoredGrant(sessB, repeatOtherSession); ok {
		t.Fatal("session grant crossed into another session")
	}

	// Project scope: same workspace, later session.
	projectDec, _ := d.kern.Request(sessA.SessionID, "CommandExec", "npm install right-pad", "")
	if _, err := d.handleApprove(mustJSON(t, map[string]any{
		"session_id": sessA.SessionID, "decision_id": projectDec.DecisionID, "approver": "operator", "scope": "project",
	})); err != nil {
		t.Fatal(err)
	}
	projectRepeat, _ := d.kern.Request(sessB.SessionID, "CommandExec", "npm install right-pad", "")
	if approved, ok := d.approveFromStoredGrant(sessB, projectRepeat); !ok || approved.Decision != "allowed" {
		t.Fatalf("project grant did not cross sessions in workspace: approved=%+v ok=%v", approved, ok)
	}

	// Deny never creates a grant.
	deniedDec, _ := d.kern.Request(sessA.SessionID, "CommandExec", "npm install denied-pad", "")
	if _, err := d.handleDeny(mustJSON(t, map[string]any{
		"session_id": sessA.SessionID, "decision_id": deniedDec.DecisionID, "approver": "operator", "reason": "no",
	})); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.approvalGrants.match(sessA, deniedDec.Capability, deniedDec.Resource); ok {
		t.Fatal("deny created an approval grant")
	}

	// Even a matching stored grant cannot rescue a kernel denial.
	destructiveGrant := approvalGrant{
		Scope: approvalScopeProject, WorkspaceRoot: workspace, Capability: "CommandExec", Resource: "rm -rf /",
		SourceDecisionID: "fixture", Approver: "operator", CreatedAt: time.Now().UTC(),
	}
	if err := d.approvalGrants.add(destructiveGrant, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	veto, _ := d.kern.Request(sessB.SessionID, "CommandExec", "rm -rf /", "")
	if veto.Decision != "denied" {
		t.Fatalf("destructive command decision = %s, want denied", veto.Decision)
	}
	if _, ok := d.approveFromStoredGrant(sessB, veto); ok {
		t.Fatal("stored grant overrode a kernel denial")
	}

	raw, err := d.kern.ReadEvents(sessA.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "approval_grant_authorized") || !strings.Contains(string(raw), "approval_grant_used") {
		t.Fatalf("scoped grant lifecycle missing from audit: %s", raw)
	}
}

func TestProjectApprovalGrantSurvivesDaemonRestart(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	stateDir := t.TempDir()
	workspace := t.TempDir()
	opts := Options{
		StateDir: stateDir, KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true,
	}

	d1, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	sessA, _ := d1.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err := d1.kern.InitSessionFull(sessA.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	dec, _ := d1.kern.Request(sessA.SessionID, "CommandExec", "npm install persistent-pad", "")
	if _, err := d1.handleApprove(mustJSON(t, map[string]any{
		"session_id": sessA.SessionID, "decision_id": dec.DecisionID, "approver": "operator", "scope": "project",
	})); err != nil {
		t.Fatal(err)
	}
	_ = d1.Close() // kernel shutdown is an intentional process kill

	d2, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	sessB, _ := d2.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	if err := d2.kern.InitSessionFull(sessB.SessionID, workspace, "safe-edit", "on_request", nil); err != nil {
		t.Fatal(err)
	}
	repeat, _ := d2.kern.Request(sessB.SessionID, "CommandExec", "npm install persistent-pad", "")
	if approved, ok := d2.approveFromStoredGrant(sessB, repeat); !ok || approved.Decision != "allowed" {
		t.Fatalf("project grant was not recovered after daemon restart: approved=%+v ok=%v", approved, ok)
	}
}

func TestPermissionRequestIsDurableForReconnect(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 10 * time.Second
	reqs := permissionRequests(d)

	sess, _ := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "install")
	dec, _ := d.kern.Request(sess.SessionID, "CommandExec", "npm install durable", task.TaskID)
	done := make(chan struct{})
	go func() {
		d.resolveApproval(sess, task, dec, "npm install durable")
		close(done)
	}()

	var decisionID string
	select {
	case decisionID = <-reqs:
	case <-time.After(5 * time.Second):
		t.Fatal("permission request was not published")
	}
	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var events []struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		request, _ := event.Payload["request"].(map[string]any)
		if event.Payload["status"] == "permission_requested" && request["decision_id"] == decisionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("durable permission request %s missing from audit", decisionID)
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{
		"decision_id": decisionID, "approve": false, "scope": "once",
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("approval wait did not resolve")
	}
}
