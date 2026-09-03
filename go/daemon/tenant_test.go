package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	carinatelemetry "github.com/Nebutra/carina/go/telemetry"
	"github.com/Nebutra/carina/go/toolchain"
)

func TestLocalUnixSocketStillWorksWithoutTenant(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sessAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws,
		"profile":        "safe-edit",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess := sessAny.(*sessionstore.Session)
	if sessionstore.NormalizeTenantID(sess.TenantID) != sessionstore.LocalTenantID {
		t.Fatalf("omitted tenant_id must default to local, got %q", sess.TenantID)
	}
	got, err := d.handleSessionGet(mustRaw(map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatalf("local session.get without tenant_id must work: %v", err)
	}
	if got == nil {
		t.Fatal("local session.get returned nil")
	}
	list, err := d.handleSessionList(mustRaw(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := list.([]sessionContinuityEntry)
	if len(entries) == 0 {
		t.Fatal("local session.list must include the created session")
	}
}

func TestTwoTenantsCannotListOrGetEachOther(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	aRoot, bRoot := t.TempDir(), t.TempDir()
	aAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": aRoot, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	bAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": bRoot, "profile": "safe-edit", "tenant_id": "org_b",
	}))
	if err != nil {
		t.Fatal(err)
	}
	a := aAny.(*sessionstore.Session)
	b := bAny.(*sessionstore.Session)

	if _, err := d.handleSessionGet(mustRaw(map[string]any{"session_id": a.SessionID, "tenant_id": "org_b"})); err == nil {
		t.Fatal("org_b must not get org_a session")
	}
	if _, err := d.handleSessionGet(mustRaw(map[string]any{"session_id": b.SessionID, "tenant_id": "org_a"})); err == nil {
		t.Fatal("org_a must not get org_b session")
	}
	listed, err := d.handleSessionList(mustRaw(map[string]any{"tenant_id": "org_a"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range listed.([]sessionContinuityEntry) {
		if entry.SessionID == b.SessionID {
			t.Fatal("org_a list leaked org_b")
		}
	}
}

func TestSharedWorkspaceRootAcrossTenantsRejected(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if _, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws, "profile": "safe-edit", "tenant_id": "org_a",
	})); err != nil {
		t.Fatal(err)
	}
	_, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws, "profile": "safe-edit", "tenant_id": "org_b",
	}))
	if err == nil || !strings.Contains(err.Error(), "another tenant") {
		t.Fatalf("shared workspace must fail closed, got %v", err)
	}
}

func TestTwoTenantsCannotReadGuessedPath(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	aRoot, bRoot := t.TempDir(), t.TempDir()
	secret := filepath.Join(bRoot, "secret.txt")
	if err := os.WriteFile(secret, []byte("nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	aAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": aRoot, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": bRoot, "profile": "safe-edit", "tenant_id": "org_b",
	})); err != nil {
		t.Fatal(err)
	}
	a := aAny.(*sessionstore.Session)
	dec, err := d.kern.Request(a.SessionID, "FileRead", secret, "t-guess")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision == "allowed" {
		t.Fatal("tenant A must not read tenant B's file by guessing the path")
	}
}

func TestGatewayTokenTenantMismatchFailsClosed(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	aRoot := t.TempDir()
	aAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": aRoot, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	a := aAny.(*sessionstore.Session)
	foreign := rpc.GatewayTokenClaims{TenantID: "org_b", Role: rpc.RoleOperator, Scopes: []rpc.Scope{rpc.ScopeRead}}
	err = d.gatewayRemoteParamsAllowed("session.get", mustRaw(map[string]any{"session_id": a.SessionID}), foreign)
	if err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("token for org_b must not see org_a, got %v", err)
	}
	own := rpc.GatewayTokenClaims{TenantID: "org_a", Role: rpc.RoleOperator, Scopes: []rpc.Scope{rpc.ScopeRead}}
	if err := d.gatewayRemoteParamsAllowed("session.get", mustRaw(map[string]any{"session_id": a.SessionID}), own); err != nil {
		t.Fatalf("matching tenant token must pass: %v", err)
	}
}

func TestTwoTenantsCannotReplayOrStartEachOther(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	aRoot, bRoot := t.TempDir(), t.TempDir()
	aAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": aRoot, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": bRoot, "profile": "safe-edit", "tenant_id": "org_b",
	})); err != nil {
		t.Fatal(err)
	}
	a := aAny.(*sessionstore.Session)
	if _, err := d.handleSessionReplay(mustRaw(map[string]any{"session_id": a.SessionID, "tenant_id": "org_b"})); err == nil {
		t.Fatal("org_b must not replay org_a")
	}
	if _, err := d.handleTaskSubmit(mustRaw(map[string]any{
		"session_id": a.SessionID, "tenant_id": "org_b", "prompt": "hi",
	})); err == nil {
		t.Fatal("org_b must not start execution on org_a")
	}
	if _, err := d.handleSecretGrant(mustRaw(map[string]any{
		"session_id": a.SessionID, "tenant_id": "org_b", "name": "K", "value": "v",
	})); err == nil {
		t.Fatal("org_b must not grant secrets on org_a")
	}
	if _, err := d.handleSessionModelGet(mustRaw(map[string]any{"session_id": a.SessionID, "tenant_id": "org_b"})); err == nil {
		t.Fatal("org_b must not read org_a model preference")
	}
	if _, err := d.handleUsageCost(mustRaw(map[string]any{"session_id": a.SessionID, "tenant_id": "org_b"})); err == nil {
		t.Fatal("org_b must not read org_a usage")
	}
	if _, err := d.handleWorkspaceTree(mustRaw(map[string]any{"session_id": a.SessionID, "tenant_id": "org_b"})); err == nil {
		t.Fatal("org_b must not list org_a workspace")
	}
	if _, err := d.handlePatchList(mustRaw(map[string]any{"session_id": a.SessionID, "tenant_id": "org_b"})); err == nil {
		t.Fatal("org_b must not list org_a patches")
	}
	if _, err := d.handleSessionRename(mustRaw(map[string]any{"session_id": a.SessionID, "tenant_id": "org_b", "name": "stolen"})); err == nil {
		t.Fatal("org_b must not rename org_a")
	}
}

func TestBoundTokenTenantFillsOmittedParams(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sessAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess := sessAny.(*sessionstore.Session)
	params := rpc.BindTenantParams(mustRaw(map[string]any{"session_id": sess.SessionID}), rpc.GatewayTokenClaims{TenantID: "org_a"})
	if _, err := d.handleSessionGet(params); err != nil {
		t.Fatalf("token tenant must fill omitted tenant_id: %v", err)
	}
	listed, err := d.handleSessionList(rpc.BindTenantParams(nil, rpc.GatewayTokenClaims{TenantID: "org_a"}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range listed.([]sessionContinuityEntry) {
		if entry.SessionID == sess.SessionID {
			found = true
		}
	}
	if !found {
		t.Fatal("bound tenant list missed the session")
	}
}

func TestConfigInventoryReportsTenantSandbox(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.sandbox.Store(false)
	sessAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess := sessAny.(*sessionstore.Session)
	raw, err := d.handleConfigInventory(mustRaw(map[string]any{"session_id": sess.SessionID, "tenant_id": "org_a"}))
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	effective := out["effective"].(map[string]any)
	if effective["tenant_sandbox"] != true || effective["sandbox_commands"] != true {
		t.Fatalf("tenant sandbox not effective: %#v", effective)
	}
}

func TestTenantRunRequiresSandbox(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.sandbox.Store(false)
	sessAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess := sessAny.(*sessionstore.Session)
	if !d.commandSandbox(sess) {
		t.Fatal("non-local tenant must force command sandbox")
	}
	local := &sessionstore.Session{TenantID: sessionstore.LocalTenantID, WorkspaceRoot: ws}
	if d.commandSandbox(local) {
		t.Fatal("local tenant must follow daemon sandbox flag when it is off")
	}
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	got := d.agentRunOutcome(sess, task, []string{"true"})
	if toolchain.InspectSandbox(true).Available {
		if got.status == "failed" && strings.Contains(got.display, "sandbox") {
			t.Fatalf("sandbox helper is available; tenant run should not fail closed on missing helper: %+v", got)
		}
	} else if got.status != "failed" || !strings.Contains(strings.ToLower(got.display), "sandbox") {
		t.Fatalf("tenant run must fail closed without a sandbox helper, got %+v", got)
	}
}

func TestTenantSandboxBlocksHostWrite(t *testing.T) {
	if !toolchain.InspectSandbox(true).Available {
		t.Skip("OS sandbox helper not available")
	}
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	sessAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess := sessAny.(*sessionstore.Session)
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run")
	_ = d.agentRunOutcome(sess, task, []string{"touch", outside})
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("tenant sandbox must block writes outside the workspace")
	}
	inside := filepath.Join(ws, "ok.txt")
	got := d.agentRunOutcome(sess, task, []string{"touch", inside})
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("tenant sandbox must allow workspace writes, outcome=%+v stat=%v", got, err)
	}
}

func TestSessionAttributionFillsTenant(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sessAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws, "profile": "safe-edit", "tenant_id": "org_a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess := sessAny.(*sessionstore.Session)
	attr := d.sessionAttribution(sess.SessionID, carinatelemetry.Attribution{RunID: "run_1"})
	if attr.TenantID != "org_a" || attr.SessionID != sess.SessionID || attr.WorkspaceID == "" {
		t.Fatalf("attribution = %+v", attr)
	}
}
