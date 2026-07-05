// Package kernel hosts the Rust Capability Kernel as a child process and
// exposes typed wrappers over its stdio JSON-RPC interface (PRD §15.1).
// The Go control plane never touches workspace files, policy, or the event
// log directly — every side effect goes through this client.
package kernel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/TsekaLuk/pi-os/go/rpc"
)

// Decision mirrors protocol/schemas/permission-decision.schema.json.
type Decision struct {
	DecisionID  string `json:"decision_id"`
	Capability  string `json:"capability"`
	RequestedBy string `json:"requested_by"`
	Resource    string `json:"resource"`
	Decision    string `json:"decision"` // allowed | denied | requires_approval
	Reason      string `json:"reason"`
	PolicyID    string `json:"policy_id"`
}

// Patch mirrors protocol/schemas/patch-transaction.schema.json.
type Patch struct {
	PatchID         string   `json:"patch_id"`
	SessionID       string   `json:"session_id"`
	Status          string   `json:"status"`
	AffectedFiles   []string `json:"affected_files"`
	BaseHash        string   `json:"base_hash"`
	NewHash         string   `json:"new_hash,omitempty"`
	Diff            string   `json:"diff"`
	Reason          string   `json:"reason"`
	RiskLevel       int      `json:"risk_level"`
	ApprovalStatus  string   `json:"approval_status"`
	TestStatus      string   `json:"test_status"`
	RollbackPointer string   `json:"rollback_pointer,omitempty"`
}

// FileChange is one file in a patch proposal (full-content MVP semantics).
type FileChange struct {
	Path       string `json:"path"`
	NewContent string `json:"new_content"`
}

// Service is a running pi-kernel-service child process.
type Service struct {
	cmd    *exec.Cmd
	client *rpc.Client
}

// Start launches the kernel binary with the given state directory.
func Start(binPath, stateDir string) (*Service, error) {
	if binPath == "" {
		var err error
		binPath, err = FindBinary()
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(binPath, stateDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("kernel: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("kernel: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("kernel: start %s: %w", binPath, err)
	}
	svc := &Service{cmd: cmd, client: rpc.NewClient(stdin, stdout, nil)}
	if err := svc.client.Call("ping", map[string]any{}, nil); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("kernel: handshake failed: %w", err)
	}
	return svc, nil
}

// FindBinary locates pi-kernel-service: $PI_KERNEL_BIN, next to the current
// executable, cargo target dirs, then $PATH.
func FindBinary() (string, error) {
	if p := os.Getenv("PI_KERNEL_BIN"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "pi-kernel-service")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	for _, rel := range []string{"target/release/pi-kernel-service", "target/debug/pi-kernel-service"} {
		if _, err := os.Stat(rel); err == nil {
			return rel, nil
		}
	}
	if p, err := exec.LookPath("pi-kernel-service"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("kernel: pi-kernel-service not found (set PI_KERNEL_BIN or run `cargo build`)")
}

func (s *Service) Close() error {
	_ = s.client.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.cmd.Wait()
}

func (s *Service) call(method string, params map[string]any, result any) error {
	return s.client.Call(method, params, result)
}

// OrgPolicy carries enterprise policy applied at session init (PRD §5).
type OrgPolicy struct {
	BundleTOML        string           // mandatory-deny policy bundle
	TrustedPluginKeys []string         // base64 ed25519 publisher keys
	ApprovalPolicy    []ApprovalRule   // role required per risk threshold
}

type ApprovalRule struct {
	MinRisk int    `json:"min_risk"`
	Role    string `json:"role"`
}

func (s *Service) InitSession(sessionID, workspaceRoot, profile string) error {
	return s.InitSessionWithPolicy(sessionID, workspaceRoot, profile, nil)
}

// InitSessionWithPolicy initializes a session and, if org is non-nil,
// attaches the org policy bundle, trusted plugin keys, and approval policy.
func (s *Service) InitSessionWithPolicy(sessionID, workspaceRoot, profile string, org *OrgPolicy) error {
	params := map[string]any{
		"session_id": sessionID, "workspace_root": workspaceRoot, "profile": profile,
	}
	if org != nil {
		if org.BundleTOML != "" {
			params["bundle_toml"] = org.BundleTOML
		}
		if len(org.TrustedPluginKeys) > 0 {
			params["trusted_plugin_keys"] = org.TrustedPluginKeys
		}
		if len(org.ApprovalPolicy) > 0 {
			params["approval_policy"] = org.ApprovalPolicy
		}
	}
	return s.call("kernel.session.init", params, nil)
}

// ApproveWithRole resolves an approval carrying the approver's role (RBAC).
func (s *Service) ApproveWithRole(sessionID, decisionID, approver, role string) (*Decision, error) {
	var d Decision
	params := map[string]any{"session_id": sessionID, "decision_id": decisionID, "approver": approver}
	if role != "" {
		params["role"] = role
	}
	err := s.call("kernel.approve", params, &d)
	return &d, err
}

// AuditExport returns the full audit bundle for centralized audit.
func (s *Service) AuditExport(sessionID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call("kernel.audit.export", map[string]any{"session_id": sessionID}, &out)
	return out, err
}

// Request evaluates a capability request and returns the audit-logged decision.
func (s *Service) Request(sessionID, capability, resource, taskID string) (*Decision, error) {
	params := map[string]any{"session_id": sessionID, "capability": capability, "resource": resource}
	if taskID != "" {
		params["task_id"] = taskID
	}
	var d Decision
	if err := s.call("kernel.request", params, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Service) Approve(sessionID, decisionID, approver string) (*Decision, error) {
	var d Decision
	err := s.call("kernel.approve", map[string]any{
		"session_id": sessionID, "decision_id": decisionID, "approver": approver,
	}, &d)
	return &d, err
}

func (s *Service) Deny(sessionID, decisionID, approver, reason string) (*Decision, error) {
	var d Decision
	err := s.call("kernel.deny", map[string]any{
		"session_id": sessionID, "decision_id": decisionID, "approver": approver, "reason": reason,
	}, &d)
	return &d, err
}

// RecordEvent appends a lifecycle event to the session's audit log, tagged
// with the language actor that produced it (go/rust/zig/model/user).
func (s *Service) RecordEvent(sessionID, eventType, taskID, actor string, payload map[string]any, decisionID string) error {
	params := map[string]any{"session_id": sessionID, "type": eventType, "payload": payload}
	if taskID != "" {
		params["task_id"] = taskID
	}
	if actor != "" {
		params["actor"] = actor
	}
	if decisionID != "" {
		params["permission_decision_id"] = decisionID
	}
	return s.call("kernel.event.record", params, nil)
}

// AuditVerify recomputes the session's hash chain and reports any tampering.
func (s *Service) AuditVerify(sessionID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call("kernel.audit.verify", map[string]any{"session_id": sessionID}, &out)
	return out, err
}

func (s *Service) ReadEvents(sessionID string) (json.RawMessage, error) {
	var events json.RawMessage
	err := s.call("kernel.audit.read", map[string]any{"session_id": sessionID}, &events)
	return events, err
}

func (s *Service) AuditReport(sessionID string) (json.RawMessage, error) {
	var report json.RawMessage
	err := s.call("kernel.audit.report", map[string]any{"session_id": sessionID}, &report)
	return report, err
}

func (s *Service) PatchPropose(sessionID, taskID, reason string, files []FileChange) (*Patch, error) {
	params := map[string]any{"session_id": sessionID, "reason": reason, "files": files}
	if taskID != "" {
		params["task_id"] = taskID
	}
	var p Patch
	err := s.call("kernel.patch.propose", params, &p)
	return &p, err
}

func (s *Service) PatchApply(sessionID, patchID, approver string) (*Patch, error) {
	var p Patch
	err := s.call("kernel.patch.apply", map[string]any{
		"session_id": sessionID, "patch_id": patchID, "approver": approver,
	}, &p)
	return &p, err
}

func (s *Service) PatchRollback(sessionID, patchID string) (*Patch, error) {
	var p Patch
	err := s.call("kernel.patch.rollback", map[string]any{"session_id": sessionID, "patch_id": patchID}, &p)
	return &p, err
}

func (s *Service) PatchList(sessionID string) ([]Patch, error) {
	var list []Patch
	err := s.call("kernel.patch.list", map[string]any{"session_id": sessionID}, &list)
	return list, err
}

func (s *Service) PatchShow(sessionID, patchID string) (*Patch, error) {
	var p Patch
	err := s.call("kernel.patch.show", map[string]any{"session_id": sessionID, "patch_id": patchID}, &p)
	return &p, err
}

func (s *Service) ClassifyCommand(command string) (int, error) {
	var out struct {
		RiskLevel int `json:"risk_level"`
	}
	err := s.call("kernel.classify", map[string]any{"command": command}, &out)
	return out.RiskLevel, err
}

// ProfileDescribe returns the capability-graph view of a session's profile.
func (s *Service) ProfileDescribe(sessionID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call("kernel.profile.describe", map[string]any{"session_id": sessionID}, &out)
	return out, err
}

// GrantSecret registers a secret value; only the handle is returned.
func (s *Service) GrantSecret(sessionID, name, value string) (string, error) {
	var out struct {
		Handle string `json:"handle"`
	}
	err := s.call("kernel.secret.grant", map[string]any{"session_id": sessionID, "name": name, "value": value}, &out)
	return out.Handle, err
}

// RequestSecret asks for a secret handle; plaintext never crosses this boundary.
func (s *Service) RequestSecret(sessionID, name string) (*Decision, string, error) {
	var out struct {
		Decision Decision `json:"decision"`
		Handle   string   `json:"handle"`
	}
	err := s.call("kernel.secret.request", map[string]any{"session_id": sessionID, "name": name}, &out)
	return &out.Decision, out.Handle, err
}

// Redact scrubs known secret values from text before it is logged.
func (s *Service) Redact(sessionID, text string) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	err := s.call("kernel.redact", map[string]any{"session_id": sessionID, "text": text}, &out)
	return out.Text, err
}

// PluginInspect parses a manifest and returns its declared permissions.
func (s *Service) PluginInspect(manifestTOML string) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call("kernel.plugin.inspect", map[string]any{"manifest_toml": manifestTOML}, &out)
	return out, err
}

// PluginRun runs a WASM plugin under the session policy. wasmBase64 is the
// base64-encoded module; signatureBase64 is an optional ed25519 signature
// (required when the deployment trusts publisher keys).
func (s *Service) PluginRun(sessionID, manifestTOML, wasmBase64, signatureBase64 string) (json.RawMessage, error) {
	var out json.RawMessage
	params := map[string]any{
		"session_id": sessionID, "manifest_toml": manifestTOML, "wasm_base64": wasmBase64,
	}
	if signatureBase64 != "" {
		params["signature_base64"] = signatureBase64
	}
	err := s.call("kernel.plugin.run", params, &out)
	return out, err
}
