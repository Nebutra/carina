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

func (s *Service) InitSession(sessionID, workspaceRoot, profile string) error {
	return s.call("kernel.session.init", map[string]any{
		"session_id": sessionID, "workspace_root": workspaceRoot, "profile": profile,
	}, nil)
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

// RecordEvent appends a lifecycle event to the session's audit log.
func (s *Service) RecordEvent(sessionID, eventType, taskID string, payload map[string]any, decisionID string) error {
	params := map[string]any{"session_id": sessionID, "type": eventType, "payload": payload}
	if taskID != "" {
		params["task_id"] = taskID
	}
	if decisionID != "" {
		params["permission_decision_id"] = decisionID
	}
	return s.call("kernel.event.record", params, nil)
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
