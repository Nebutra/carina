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
	"unicode"

	"github.com/Nebutra/carina/go/kernel"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	approvalScopeOnce    = "once"
	approvalScopeSession = "session"
	approvalScopeProject = "project"

	approvalMatchExact  = "exact"
	approvalMatchPrefix = "prefix"
)

type approvalGrant struct {
	Scope            string    `json:"scope"`
	SessionID        string    `json:"session_id,omitempty"`
	WorkspaceRoot    string    `json:"workspace_root,omitempty"`
	Capability       string    `json:"capability"`
	Resource         string    `json:"resource"`
	Match            string    `json:"match,omitempty"` // exact (default) | prefix
	SourceDecisionID string    `json:"source_decision_id"`
	Approver         string    `json:"approver"`
	Role             string    `json:"role,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// approvalGrantStore persists narrowly scoped approval grants. Matching is
// exact by default. Path capabilities (FileRead/FileWrite) may also install a
// safe directory prefix grant for session/project scopes so repeated edits
// under the same folder do not re-prompt. Prefix grants never cover dangerous
// resources and never widen what the capability kernel considers denied.
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

func normalizeApprovalMatch(match string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(match)) {
	case "", approvalMatchExact:
		return approvalMatchExact, nil
	case approvalMatchPrefix:
		return approvalMatchPrefix, nil
	default:
		return "", fmt.Errorf("invalid approval match %q: want exact or prefix", match)
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
			// Match workspace-root normalization so macOS /var vs /private/var
			// and similar symlink forms do not break under-workspace checks.
			return normalizeExistingPath(resource)
		}
	}
	return resource
}

func normalizeWorkspaceRoot(root string) string {
	return normalizeExistingPath(root)
}

// normalizeExistingPath Abs+Cleans a path and resolves symlinks through the
// longest existing ancestor. Non-existent leaf files still inherit their
// parent's resolved form (required on macOS where /var → /private/var).
func normalizeExistingPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real)
	}
	// Walk up until an ancestor resolves, then rejoin the missing suffix.
	cur := abs
	var suffix []string
	for {
		dir := filepath.Dir(cur)
		if dir == cur {
			break
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			parts := append([]string{real}, suffix...)
			return filepath.Clean(filepath.Join(parts...))
		}
		cur = dir
	}
	return abs
}

func normalizeApprovalGrant(grant approvalGrant) (approvalGrant, error) {
	scope, err := normalizeApprovalScope(grant.Scope)
	if err != nil || scope == approvalScopeOnce {
		return approvalGrant{}, fmt.Errorf("persistent approval grant requires session or project scope")
	}
	match, err := normalizeApprovalMatch(grant.Match)
	if err != nil {
		return approvalGrant{}, err
	}
	grant.Scope = scope
	grant.Match = match
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
	if match == approvalMatchPrefix {
		if err := validatePrefixGrant(grant.Capability, grant.Resource, grant.WorkspaceRoot); err != nil {
			return approvalGrant{}, err
		}
	}
	return grant, nil
}

func approvalGrantKey(grant approvalGrant) string {
	owner := grant.SessionID
	if grant.Scope == approvalScopeProject {
		owner = grant.WorkspaceRoot
	}
	match := grant.Match
	if match == "" {
		match = approvalMatchExact
	}
	return grant.Scope + "\x00" + owner + "\x00" + grant.Capability + "\x00" + match + "\x00" + grant.Resource
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

	// Dangerous resources never auto-satisfy from a stored grant (exact or
	// prefix). The operator must re-approve each such decision.
	if isDangerousApprovalResource(capability, resource) {
		return approvalGrant{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Prefer exact matches over prefix matches for audit clarity.
	var prefixHit approvalGrant
	prefixOK := false
	for _, grant := range s.grants {
		if grant.Capability != capability {
			continue
		}
		if !grantOwnsSession(grant, sess.SessionID, workspace) {
			continue
		}
		match := grant.Match
		if match == "" {
			match = approvalMatchExact
		}
		switch match {
		case approvalMatchExact:
			if grant.Resource == resource {
				return grant, true
			}
		case approvalMatchPrefix:
			if !prefixOK && resourceMatchesPrefixGrant(capability, grant.Resource, resource, workspace) {
				prefixHit = grant
				prefixOK = true
			}
		}
	}
	if prefixOK {
		return prefixHit, true
	}
	return approvalGrant{}, false
}

func grantOwnsSession(grant approvalGrant, sessionID, workspace string) bool {
	if grant.Scope == approvalScopeSession && grant.SessionID == sessionID {
		return true
	}
	if grant.Scope == approvalScopeProject && grant.WorkspaceRoot == workspace {
		return true
	}
	return false
}

func (d *Daemon) rememberApprovalGrant(sess *sessionstore.Session, dec *kernel.Decision, scope, approver, role string) error {
	scope, err := normalizeApprovalScope(scope)
	if err != nil || scope == approvalScopeOnce {
		return err
	}
	// Exact grant first (canonical re-run of the same resource).
	if err := d.addApprovalGrant(sess, dec, scope, approvalMatchExact, dec.Resource, approver, role); err != nil {
		return err
	}
	// Safe path-prefix companion for session/project file grants: repeated
	// edits under the same directory stop re-prompting without widening to
	// the whole workspace or to dangerous paths.
	if prefix, ok := deriveSafePathPrefixGrant(dec.Capability, dec.Resource, sess.WorkspaceRoot); ok {
		if err := d.addApprovalGrant(sess, dec, scope, approvalMatchPrefix, prefix, approver, role); err != nil {
			// Exact grant already installed; prefix is best-effort fatigue
			// reduction and must not fail the operator's approval path.
			d.record(sess.SessionID, "ToolApproved", "", "go", map[string]any{
				"status": "approval_prefix_grant_skipped", "error": err.Error(),
				"capability": normalizeApprovalCapability(dec.Capability), "resource": prefix,
			}, dec.DecisionID)
		}
	}
	return nil
}

func (d *Daemon) addApprovalGrant(sess *sessionstore.Session, dec *kernel.Decision, scope, match, resource, approver, role string) error {
	grant := approvalGrant{
		Scope:            scope,
		Match:            match,
		SessionID:        sess.SessionID,
		WorkspaceRoot:    sess.WorkspaceRoot,
		Capability:       dec.Capability,
		Resource:         resource,
		SourceDecisionID: dec.DecisionID,
		Approver:         approver,
		Role:             role,
		CreatedAt:        time.Now().UTC(),
	}
	auditPayload := map[string]any{
		"status":             "approval_grant_authorized",
		"scope":              scope,
		"match":              match,
		"capability":         normalizeApprovalCapability(dec.Capability),
		"resource":           normalizeApprovalResource(dec.Capability, resource),
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
	var cursor int
	if err := d.approvalGrants.add(grant, func() error {
		var err error
		cursor, err = d.kern.RecordEventWithCursor(sess.SessionID, "ToolApproved", "", "user", auditPayload, dec.DecisionID)
		return err
	}); err != nil {
		return err
	}
	d.events.Publish(sess.SessionID, map[string]any{
		"session_id":           sess.SessionID,
		"type":                 "ToolApproved",
		"actor":                "user",
		"timestamp":            time.Now().UTC().Format(time.RFC3339),
		"payload":              auditPayload,
		internalRawAuditCursor: cursor,
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
	match := grant.Match
	if match == "" {
		match = approvalMatchExact
	}
	d.record(sess.SessionID, "ToolApproved", "", "go", map[string]any{
		"status":             "approval_grant_used",
		"scope":              grant.Scope,
		"match":              match,
		"capability":         grant.Capability,
		"resource":           grant.Resource,
		"source_decision_id": grant.SourceDecisionID,
		"approver":           grant.Approver,
	}, dec.DecisionID)
	return approved, true
}

// ---- prefix grants + dangerous list ---------------------------------------

func isPathCapability(capability string) bool {
	switch normalizeApprovalCapability(capability) {
	case "fileread", "filewrite":
		return true
	default:
		return false
	}
}

// deriveSafePathPrefixGrant returns a directory prefix for FileRead/FileWrite
// session/project companions. Empty / workspace-root / dangerous paths are
// refused so a single approval cannot become a whole-tree auto-allow.
func deriveSafePathPrefixGrant(capability, resource, workspaceRoot string) (string, bool) {
	if !isPathCapability(capability) {
		return "", false
	}
	resource = normalizeApprovalResource(capability, resource)
	if resource == "" || resource == "." || resource == string(filepath.Separator) {
		return "", false
	}
	dir := filepath.Dir(resource)
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return "", false
	}
	workspace := normalizeWorkspaceRoot(workspaceRoot)
	if workspace != "" {
		ws := filepath.Clean(workspace)
		if dir == ws || !pathIsUnderOrEqual(dir, ws) {
			// Refuse workspace-root-wide and out-of-workspace prefixes.
			return "", false
		}
	}
	if isDangerousApprovalResource(capability, dir) || isDangerousApprovalResource(capability, resource) {
		return "", false
	}
	// Ensure trailing separator semantics via Clean path + HasPrefix boundary.
	return dir, true
}

func validatePrefixGrant(capability, resource, workspaceRoot string) error {
	if !isPathCapability(capability) {
		return fmt.Errorf("prefix approval grants are only supported for FileRead/FileWrite")
	}
	if isDangerousApprovalResource(capability, resource) {
		return fmt.Errorf("prefix approval grant refused for dangerous resource %q", resource)
	}
	workspace := normalizeWorkspaceRoot(workspaceRoot)
	if workspace != "" {
		ws := filepath.Clean(workspace)
		clean := filepath.Clean(resource)
		if clean == ws || clean == string(filepath.Separator) || clean == "." {
			return fmt.Errorf("prefix approval grant cannot cover the whole workspace")
		}
		if !pathIsUnderOrEqual(clean, ws) {
			return fmt.Errorf("prefix approval grant must stay inside the workspace")
		}
	}
	return nil
}

func resourceMatchesPrefixGrant(capability, grantResource, requestResource, workspace string) bool {
	if !isPathCapability(capability) {
		return false
	}
	grantResource = filepath.Clean(grantResource)
	requestResource = filepath.Clean(requestResource)
	if grantResource == "" || requestResource == "" {
		return false
	}
	if isDangerousApprovalResource(capability, requestResource) {
		return false
	}
	if workspace != "" {
		ws := filepath.Clean(workspace)
		// Prefix must not be the workspace root (too wide).
		if grantResource == ws {
			return false
		}
		if !pathIsUnderOrEqual(grantResource, ws) || !pathIsUnderOrEqual(requestResource, ws) {
			return false
		}
	}
	return pathIsUnderOrEqual(requestResource, grantResource)
}

// pathIsUnderOrEqual reports whether child is equal to parent or a path under it.
func pathIsUnderOrEqual(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	sep := string(filepath.Separator)
	prefix := parent
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(child, prefix)
}

// isDangerousApprovalResource blocks grant auto-satisfy (and prefix install)
// for high-blast-radius resources. Exact interactive approval is still allowed
// via the normal operator prompt; only stored reuse is refused.
func isDangerousApprovalResource(capability, resource string) bool {
	cap := normalizeApprovalCapability(capability)
	res := strings.TrimSpace(resource)
	if res == "" {
		return false
	}
	lower := strings.ToLower(res)

	// Capability-level: secrets / remote never auto-reuse via grants.
	switch cap {
	case "secretread", "secretwrite", "secretgrant", "remoteexecute":
		return true
	}

	// Path danger markers — file capabilities only (avoid false positives on
	// ordinary command strings like `echo secret_token_name`).
	if cap == "fileread" || cap == "filewrite" {
		pathMarkers := []string{
			".env", ".ssh", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
			"credentials", ".aws", ".gnupg", ".kube",
			"authorized_keys", "known_hosts", ".netrc", ".npmrc", ".pypirc",
			"private_key", "privatekey", "wallet.dat",
		}
		// Segment markers matched only as path segments / basenames.
		segmentMarkers := []string{"secret", "secrets", "credentials"}
		base := strings.ToLower(filepath.Base(res))
		for _, m := range pathMarkers {
			if base == m || strings.Contains(lower, "/"+m) || strings.Contains(lower, "\\"+m) {
				return true
			}
		}
		for _, m := range segmentMarkers {
			if base == m || strings.Contains(lower, "/"+m+"/") || strings.Contains(lower, "\\"+m+"\\") ||
				strings.HasSuffix(lower, "/"+m) || strings.HasSuffix(lower, "\\"+m) {
				return true
			}
		}
	}

	if cap == "commandexec" || cap == "shell" {
		return isDangerousCommandResource(lower)
	}
	return false
}

func isDangerousCommandResource(lower string) bool {
	// First token (binary) denylist for prefix/exact grant reuse.
	first := firstCommandToken(lower)
	dangerousBins := map[string]bool{
		"rm": true, "rmdir": true, "dd": true, "mkfs": true, "mkfs.ext4": true,
		"mkfs.xfs": true, "shutdown": true, "reboot": true, "halt": true,
		"poweroff": true, "sudo": true, "doas": true, "su": true,
		"chmod": true, "chown": true, "chgrp": true, "userdel": true,
		"useradd": true, "passwd": true, "visudo": true, "crontab": true,
		"launchctl": true, "systemctl": true, "service": true,
		"iptables": true, "pfctl": true, "kill": true, "killall": true,
		"pkill": true, "curl": true, "wget": true, "nc": true, "ncat": true,
		"netcat": true, "ssh": true, "scp": true, "rsync": true,
		"python": true, "python3": true, "perl": true, "ruby": true,
		"node": true, "bash": true, "sh": true, "zsh": true, "fish": true,
		"osascript": true, "diskutil": true, "fdisk": true, "parted": true,
		"mount": true, "umount": true, "format": true,
	}
	if dangerousBins[first] {
		return true
	}

	// Pattern denylist for compound / shell-meta danger even when the first
	// token looks tame (e.g. "npm install && rm -rf /").
	patterns := []string{
		"rm -rf", "rm -fr", "rm -r ", "mkfs", "dd if=", "dd of=",
		">/dev/", "> /dev/", "curl |", "curl|", "wget |", "wget|",
		"| sh", "|sh", "| bash", "|bash", "| zsh",
		"bash -c", "sh -c", "zsh -c", "eval ", "base64 -d",
		":(){", "fork bomb", "chmod 777", "chmod -r 777",
		"chown -r", "sudo ", "doas ", "mkfs.",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func firstCommandToken(resource string) string {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return ""
	}
	// Split on whitespace; strip common path prefixes for basename match.
	fields := strings.FieldsFunc(resource, func(r rune) bool {
		return unicode.IsSpace(r)
	})
	if len(fields) == 0 {
		return ""
	}
	tok := fields[0]
	tok = strings.Trim(tok, `'"`)
	// ./bin/foo -> foo
	if i := strings.LastIndexAny(tok, `/\`); i >= 0 && i+1 < len(tok) {
		tok = tok[i+1:]
	}
	return strings.ToLower(tok)
}
