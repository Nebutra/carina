package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func TestGatewayWorkspacePinRejectsForeignRoot(t *testing.T) {
	d, pin := newLoopDaemon(t)
	defer d.Close()
	if err := d.configureGatewayWorkspacePin(pin); err != nil {
		t.Fatal(err)
	}
	if err := d.gatewayWorkspaceAllowed(pin); err != nil {
		t.Fatalf("pinned workspace must be allowed: %v", err)
	}
	other := t.TempDir()
	err := d.gatewayWorkspaceAllowed(other)
	if err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("foreign workspace must fail closed, got %v", err)
	}
	sess, err := d.store.CreateSession(other, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.gatewaySessionAllowed(sess.SessionID); err == nil {
		t.Fatal("session outside the pin must fail closed")
	}
	pinnedSess, err := d.store.CreateSession(pin, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.gatewaySessionAllowed(pinnedSess.SessionID); err != nil {
		t.Fatalf("session inside the pin must pass: %v", err)
	}
}

func TestGatewayWorkspaceUnpinnedAllowsAnyExistingRoot(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	if err := d.gatewayWorkspaceAllowed(t.TempDir()); err != nil {
		t.Fatalf("unpinned gateway must not invent a pin: %v", err)
	}
	report := d.gatewayDoctor()
	if report["workspace_pin"] != false {
		t.Fatalf("doctor pin = %#v", report)
	}
}

func TestGatewayWorkspacePinRejectsMissingDir(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	err := d.configureGatewayWorkspacePin("/no/such/gateway-pin")
	if err == nil || !strings.Contains(err.Error(), "not an existing directory") {
		t.Fatalf("missing pin must fail closed, got %v", err)
	}
}

func TestGatewayRemotePinLeavesUnixSessionCreateOpen(t *testing.T) {
	d, pin := newLoopDaemon(t)
	defer d.Close()
	if err := d.configureGatewayWorkspacePin(pin); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	sessAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": foreign,
		"profile":        "safe-edit",
	}))
	if err != nil {
		t.Fatalf("unix/local session.create must stay unpinned: %v", err)
	}
	foreignSess, ok := sessAny.(*sessionstore.Session)
	if !ok || foreignSess == nil {
		t.Fatalf("session type %T", sessAny)
	}
	localClaims := rpc.GatewayTokenClaims{TenantID: sessionstore.LocalTenantID, Role: rpc.RoleOperator, Scopes: []rpc.Scope{rpc.ScopeRead}}
	if err := d.gatewayRemoteParamsAllowed("session.get", mustRaw(map[string]any{
		"session_id": foreignSess.SessionID,
	}), localClaims); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("remote session.get outside the pin must fail closed, got %v", err)
	}
	if err := d.gatewayRemoteParamsAllowed("session.list", nil, localClaims); err == nil || !strings.Contains(err.Error(), "session.list") {
		t.Fatalf("remote session.list must fail closed when pinned, got %v", err)
	}
	if err := d.gatewayRemoteParamsAllowed("execution.list", nil, localClaims); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("unscoped remote execution.list must fail closed, got %v", err)
	}
	if err := d.gatewayRemoteParamsAllowed("daemon.doctor", nil, rpc.GatewayTokenClaims{}); err != nil {
		t.Fatalf("doctor must stay readable on a pinned gateway: %v", err)
	}
	if err := d.gatewayRemoteParamsAllowed("work.poll", mustRaw(map[string]any{"worker_id": "w1"}), rpc.GatewayTokenClaims{}); err != nil {
		t.Fatalf("worker protocol must stay exempt: %v", err)
	}

	pinnedAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": pin,
		"profile":        "safe-edit",
	}))
	if err != nil {
		t.Fatal(err)
	}
	pinnedSess := pinnedAny.(*sessionstore.Session)
	if err := d.gatewayRemoteParamsAllowed("session.get", mustRaw(map[string]any{
		"session_id": pinnedSess.SessionID,
	}), localClaims); err != nil {
		t.Fatalf("remote session.get inside the pin must pass: %v", err)
	}
	if err := d.gatewayRemoteParamsAllowed("agent.list", mustRaw(map[string]any{
		"workspace_root": pin,
	}), localClaims); err != nil {
		t.Fatalf("bound agent.list must pass: %v", err)
	}
}

func TestGatewayRemoteParamsRequireTenantForSessionList(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	if err := d.gatewayRemoteParamsAllowed("session.list", json.RawMessage(`{}`), rpc.GatewayTokenClaims{}); err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("unscoped remote session.list without tenant must fail closed, got %v", err)
	}
	claims := rpc.GatewayTokenClaims{TenantID: "org_a", Role: rpc.RoleOperator, Scopes: []rpc.Scope{rpc.ScopeRead}}
	if err := d.gatewayRemoteParamsAllowed("session.list", json.RawMessage(`{}`), claims); err != nil {
		t.Fatalf("tenant-bound remote session.list must pass when unpinned: %v", err)
	}
}
