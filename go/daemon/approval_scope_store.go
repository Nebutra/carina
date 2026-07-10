package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/kernel"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	approvalScopeOnce    = "once"
	approvalScopeSession = "session"
	approvalScopeProject = "project"
)

type approvalGrant struct {
	Scope            string    `json:"scope"`
	SessionID        string    `json:"session_id,omitempty"`
	WorkspaceRoot    string    `json:"workspace_root,omitempty"`
	Capability       string    `json:"capability"`
	Resource         string    `json:"resource"`
	SourceDecisionID string    `json:"source_decision_id"`
	Approver         string    `json:"approver"`
	Role             string    `json:"role,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// approvalGrantStore persists narrowly scoped approval grants. Matching is
// exact after normalization: a grant can remove a repeated human prompt, but
// it never changes what the capability kernel considers denied.
type approvalGrantStore struct {
	mu     sync.Mutex
	path   string
	grants []approvalGrant
}

func newApprovalGrantStore(stateDir string) *approvalGrantStore {
	s := &approvalGrantStore{path: filepath.Join(stateDir, "approval-grants.json")}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var stored []approvalGrant
	if json.Unmarshal(raw, &stored) != nil {
		return s
	}
	for _, grant := range stored {
		if normalized, err := normalizeApprovalGrant(grant); err == nil {
			s.grants = append(s.grants, normalized)
		}
	}
	return s
}

func normalizeApprovalScope(scope string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", approvalScopeOnce:
		return approvalScopeOnce, nil
	case approvalScopeSession:
		return approvalScopeSession, nil
	case approvalScopeProject:
		return approvalScopeProject, nil
	default:
		return "", fmt.Errorf("invalid approval scope %q: want once, session, or project", scope)
	}
}

func normalizeApprovalCapability(capability string) string {
	return strings.ToLower(strings.TrimSpace(capability))
}

func normalizeApprovalResource(capability, resource string) string {
	resource = strings.TrimSpace(resource)
	switch normalizeApprovalCapability(capability) {
	case "fileread", "filewrite":
		if resource != "" {
			return filepath.Clean(resource)
		}
	}
	return resource
}

func normalizeWorkspaceRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	root = filepath.Clean(root)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = filepath.Clean(real)
	}
	return root
}

func normalizeApprovalGrant(grant approvalGrant) (approvalGrant, error) {
	scope, err := normalizeApprovalScope(grant.Scope)
	if err != nil || scope == approvalScopeOnce {
		return approvalGrant{}, fmt.Errorf("persistent approval grant requires session or project scope")
	}
	grant.Scope = scope
	grant.Capability = normalizeApprovalCapability(grant.Capability)
	grant.Resource = normalizeApprovalResource(grant.Capability, grant.Resource)
	grant.WorkspaceRoot = normalizeWorkspaceRoot(grant.WorkspaceRoot)
	grant.SessionID = strings.TrimSpace(grant.SessionID)
	if grant.Capability == "" || grant.Resource == "" {
		return approvalGrant{}, fmt.Errorf("approval grant capability and resource are required")
	}
	if scope == approvalScopeSession && grant.SessionID == "" {
		return approvalGrant{}, fmt.Errorf("session approval grant requires session_id")
	}
	if scope == approvalScopeProject && grant.WorkspaceRoot == "" {
		return approvalGrant{}, fmt.Errorf("project approval grant requires workspace_root")
	}
	return grant, nil
}

func approvalGrantKey(grant approvalGrant) string {
	owner := grant.SessionID
	if grant.Scope == approvalScopeProject {
		owner = grant.WorkspaceRoot
	}
	return grant.Scope + "\x00" + owner + "\x00" + grant.Capability + "\x00" + grant.Resource
}

// add writes a candidate snapshot before authorize is called, then atomically
// publishes it. A grant therefore cannot become visible or durable unless its
// authorization audit append succeeded first.
func (s *approvalGrantStore) add(grant approvalGrant, authorize func() error) error {
	grant, err := normalizeApprovalGrant(grant)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next := append([]approvalGrant(nil), s.grants...)
	key := approvalGrantKey(grant)
	replaced := false
	for i := range next {
		if approvalGrantKey(next[i]) == key {
			next[i] = grant
			replaced = true
			break
		}
	}
	if !replaced {
		next = append(next, grant)
	}
	sort.Slice(next, func(i, j int) bool { return approvalGrantKey(next[i]) < approvalGrantKey(next[j]) })
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("approval grants: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("approval grants: create state dir: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("approval grants: write: %w", err)
	}
	defer os.Remove(tmp)
	if authorize != nil {
		if err := authorize(); err != nil {
			return fmt.Errorf("approval grants: audit authorization: %w", err)
		}
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("approval grants: publish: %w", err)
	}
	s.grants = next
	return nil
}

func (s *approvalGrantStore) match(sess *sessionstore.Session, capability, resource string) (approvalGrant, bool) {
	if s == nil || sess == nil {
		return approvalGrant{}, false
	}
	capability = normalizeApprovalCapability(capability)
	resource = normalizeApprovalResource(capability, resource)
	workspace := normalizeWorkspaceRoot(sess.WorkspaceRoot)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, grant := range s.grants {
		if grant.Capability != capability || grant.Resource != resource {
			continue
		}
		if grant.Scope == approvalScopeSession && grant.SessionID == sess.SessionID {
			return grant, true
		}
		if grant.Scope == approvalScopeProject && grant.WorkspaceRoot == workspace {
			return grant, true
		}
	}
	return approvalGrant{}, false
}

func (d *Daemon) rememberApprovalGrant(sess *sessionstore.Session, dec *kernel.Decision, scope, approver, role string) error {
	scope, err := normalizeApprovalScope(scope)
	if err != nil || scope == approvalScopeOnce {
		return err
	}
	grant := approvalGrant{
		Scope:            scope,
		SessionID:        sess.SessionID,
		WorkspaceRoot:    sess.WorkspaceRoot,
		Capability:       dec.Capability,
		Resource:         dec.Resource,
		SourceDecisionID: dec.DecisionID,
		Approver:         approver,
		Role:             role,
		CreatedAt:        time.Now().UTC(),
	}
	auditPayload := map[string]any{
		"status":             "approval_grant_authorized",
		"scope":              scope,
		"capability":         normalizeApprovalCapability(dec.Capability),
		"resource":           normalizeApprovalResource(dec.Capability, dec.Resource),
		"source_decision_id": dec.DecisionID,
		"approver":           approver,
	}
	if role != "" {
		auditPayload["role"] = role
	}
	if scope == approvalScopeProject {
		auditPayload["workspace_root"] = normalizeWorkspaceRoot(sess.WorkspaceRoot)
	} else {
		auditPayload["session_id"] = sess.SessionID
	}
	if err := d.approvalGrants.add(grant, func() error {
		return d.kern.RecordEvent(sess.SessionID, "ToolApproved", "", "user", auditPayload, dec.DecisionID)
	}); err != nil {
		return err
	}
	d.events.Publish(sess.SessionID, map[string]any{
		"session_id": sess.SessionID,
		"type":       "ToolApproved",
		"actor":      "user",
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"payload":    auditPayload,
	})
	return nil
}

// approveFromStoredGrant is called only after the kernel returned
// requires_approval. It asks the kernel to resolve that exact pending decision;
// a policy denial or role rejection is never overwritten by the daemon.
func (d *Daemon) approveFromStoredGrant(sess *sessionstore.Session, dec *kernel.Decision) (*kernel.Decision, bool) {
	if dec == nil || dec.Decision != "requires_approval" {
		return dec, false
	}
	grant, ok := d.approvalGrants.match(sess, dec.Capability, dec.Resource)
	if !ok {
		return dec, false
	}
	approved, err := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, "approval-grant:"+grant.Approver, grant.Role)
	if err != nil || approved.Decision != "allowed" {
		return dec, false
	}
	d.record(sess.SessionID, "ToolApproved", "", "go", map[string]any{
		"status":             "approval_grant_used",
		"scope":              grant.Scope,
		"capability":         grant.Capability,
		"resource":           grant.Resource,
		"source_decision_id": grant.SourceDecisionID,
		"approver":           grant.Approver,
	}, dec.DecisionID)
	return approved, true
}
