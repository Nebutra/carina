package daemon_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/rpc"
)

// TestEnterprisePolicyBundleAndRBAC verifies PRD §5 Phase 5: an org policy
// bundle tightens every session, role-based approval is enforced, and the
// audit bundle can be exported.
func TestEnterprisePolicyBundleAndRBAC(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}

	stateDir := t.TempDir()
	ws := t.TempDir()

	// Configure an org policy: cap command risk at 1 and require the
	// 'security-lead' role to approve anything at risk >= 2.
	policyDir := filepath.Join(stateDir, "policy")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(policyDir, "bundle.toml"), []byte(`
name = "acme"
max_command_risk = 1
`), 0o600)
	os.WriteFile(filepath.Join(policyDir, "approval.json"), []byte(`[{"min_risk":2,"role":"security-lead"}]`), 0o600)

	d, err := daemon.New(daemon.Options{
		StateDir: stateDir, KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"),
		PolicyDir: policyDir, Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)
	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// full-workspace is permissive, but the org bundle caps command risk at 1.
	var sess struct {
		SessionID string `json:"session_id"`
	}
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "full-workspace"}, &sess); err != nil {
		t.Fatal(err)
	}

	// A risk-2 command (package install) is denied by the org bundle even
	// though full-workspace alone would allow it with approval.
	var install struct {
		Decision struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		} `json:"decision"`
	}
	if err := c.Call("command.exec", map[string]any{"session_id": sess.SessionID, "argv": []string{"npm", "install", "left-pad"}}, &install); err != nil {
		t.Fatal(err)
	}
	if install.Decision.Decision != "denied" {
		t.Fatalf("org bundle should deny risk-2 command, got %q (%s)", install.Decision.Decision, install.Decision.Reason)
	}

	// Audit export returns the full bundle.
	var export struct {
		SessionID  string `json:"session_id"`
		EventCount int    `json:"event_count"`
		Events     []json.RawMessage
	}
	if err := c.Call("audit.export", map[string]any{"session_id": sess.SessionID}, &export); err != nil {
		t.Fatal(err)
	}
	if export.SessionID != sess.SessionID || export.EventCount == 0 {
		t.Fatalf("unexpected audit export: %+v", export)
	}
}

// TestRBACApprovalRequiresRole checks that a role-gated approval is rejected
// without the required role and accepted with it.
func TestRBACApprovalRequiresRole(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	stateDir := t.TempDir()
	ws := t.TempDir()
	policyDir := filepath.Join(stateDir, "policy")
	os.MkdirAll(policyDir, 0o700)
	// Require 'lead' role for any command at risk >= 2. No bundle cap, so the
	// command reaches the approval stage instead of being denied outright.
	os.WriteFile(filepath.Join(policyDir, "approval.json"), []byte(`[{"min_risk":2,"role":"lead"}]`), 0o600)

	d, err := daemon.New(daemon.Options{
		StateDir: stateDir, KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), PolicyDir: policyDir, Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)
	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var sess struct {
		SessionID string `json:"session_id"`
	}
	// safe-edit's command ceiling is risk 1, so a risk-2 package install
	// reaches requires_approval — where the RBAC role gate applies.
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "safe-edit"}, &sess); err != nil {
		t.Fatal(err)
	}
	var exec struct {
		Decision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"decision"`
	}
	if err := c.Call("command.exec", map[string]any{"session_id": sess.SessionID, "argv": []string{"npm", "install", "x"}}, &exec); err != nil {
		t.Fatal(err)
	}
	if exec.Decision.Decision != "requires_approval" {
		t.Fatalf("expected requires_approval, got %q", exec.Decision.Decision)
	}

	// Approve WITHOUT the role → rejected.
	var noRole struct {
		Decision struct {
			Decision string `json:"decision"`
		} `json:"decision"`
	}
	if err := c.Call("task.action.approve", map[string]any{"session_id": sess.SessionID, "decision_id": exec.Decision.DecisionID, "approver": "alice"}, &noRole); err != nil {
		t.Fatal(err)
	}
	if noRole.Decision.Decision == "allowed" {
		t.Fatal("approval without the required role must not be allowed")
	}

	// Approve WITH the role → allowed and executes.
	var withRole struct {
		Decision struct {
			Decision string `json:"decision"`
		} `json:"decision"`
	}
	if err := c.Call("task.action.approve", map[string]any{"session_id": sess.SessionID, "decision_id": exec.Decision.DecisionID, "approver": "bob", "role": "lead"}, &withRole); err != nil {
		t.Fatal(err)
	}
	if withRole.Decision.Decision != "allowed" {
		t.Fatalf("approval with role should be allowed, got %q", withRole.Decision.Decision)
	}
}
