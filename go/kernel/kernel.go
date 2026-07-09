// Package kernel hosts the Rust Capability Kernel as a child process and
// exposes typed wrappers over its stdio JSON-RPC interface (PRD §15.1).
// The Go control plane never touches workspace files, policy, or the event
// log directly — every side effect goes through this client.
package kernel

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Nebutra/carina/go/rpc"
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
	TaskID          string   `json:"task_id,omitempty"`
	AgentStepID     string   `json:"agent_step_id,omitempty"`
	ModelID         string   `json:"model_id,omitempty"`
	CreatedAt       string   `json:"created_at"`
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

// Service is a running carina-kernel-service child process.
type Service struct {
	cmd    *exec.Cmd
	client *rpc.Client
}

// Start launches the kernel binary with the given state directory. toolsDir
// is passed through as CARINA_TOOLS_DIR so the kernel can delegate patch writes
// to carina-patch-native (PRD §4.4).
func Start(binPath, stateDir, toolsDir string) (*Service, error) {
	if binPath == "" {
		var err error
		binPath, err = FindBinary()
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(binPath, stateDir)
	cmd.Env = os.Environ()
	if toolsDir != "" {
		cmd.Env = append(cmd.Env, "CARINA_TOOLS_DIR="+toolsDir)
	}
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

// FindBinary locates carina-kernel-service: $CARINA_KERNEL_BIN, next to the current
// executable, cargo target dirs, then $PATH.
func FindBinary() (string, error) {
	if p := os.Getenv("CARINA_KERNEL_BIN"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "carina-kernel-service")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	for _, rel := range []string{"target/release/carina-kernel-service", "target/debug/carina-kernel-service"} {
		if _, err := os.Stat(rel); err == nil {
			return rel, nil
		}
	}
	if p, err := exec.LookPath("carina-kernel-service"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("kernel: carina-kernel-service not found (set CARINA_KERNEL_BIN or run `cargo build`)")
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
	BundleTOML        string         // mandatory-deny policy bundle
	TrustedPluginKeys []string       // base64 ed25519 publisher keys
	ApprovalPolicy    []ApprovalRule // role required per risk threshold
	ApprovalMode      string         // untrusted | on_request | never (goal axis)
}

type ApprovalRule struct {
	MinRisk int    `json:"min_risk"`
	Role    string `json:"role"`
}

func (s *Service) InitSession(sessionID, workspaceRoot, profile string) error {
	return s.InitSessionFull(sessionID, workspaceRoot, profile, "", nil)
}

// InitSessionWithPolicy keeps the existing enterprise-only signature.
func (s *Service) InitSessionWithPolicy(sessionID, workspaceRoot, profile string, org *OrgPolicy) error {
	return s.InitSessionFull(sessionID, workspaceRoot, profile, "", org)
}

// InitSessionFull initializes a session with a per-session approval mode
// (goal axis) plus optional enterprise org policy.
func (s *Service) InitSessionFull(sessionID, workspaceRoot, profile, approvalMode string, org *OrgPolicy) error {
	params := map[string]any{
		"session_id": sessionID, "workspace_root": workspaceRoot, "profile": profile,
	}
	if approvalMode != "" {
		params["approval_mode"] = approvalMode
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
		if approvalMode == "" && org.ApprovalMode != "" {
			params["approval_mode"] = org.ApprovalMode
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

// AddDir grants the session an additional allowed root (the /add-dir scoped
// grant). Path capabilities on resources within it are thereafter evaluated as
// in-workspace, without loosening the profile.
func (s *Service) AddDir(sessionID, path string) error {
	return s.call("kernel.session.add_dir",
		map[string]any{"session_id": sessionID, "path": path}, nil)
}

// ApproveForSession approves and remembers for the whole session, so
// later requests for the same capability+resource-prefix auto-satisfy.
func (s *Service) ApproveForSession(sessionID, decisionID, approver string) (*Decision, error) {
	return s.ApproveForSessionWithJustification(sessionID, decisionID, approver, "approved for session")
}

// ApproveForSessionWithJustification approves and installs an auditable
// approval overlay for future matching requires_approval decisions.
func (s *Service) ApproveForSessionWithJustification(sessionID, decisionID, approver, justification string) (*Decision, error) {
	var d Decision
	err := s.call("kernel.approve", map[string]any{
		"session_id": sessionID, "decision_id": decisionID, "approver": approver, "for_session": true, "justification": justification,
	}, &d)
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

// IndexReport is the result of kernel.index.build / kernel.index.update
// (docs/plans/code-intelligence.md). Skipped carries the paths the kernel
// refused to ingest (policy-denied or unsupported) with the reason.
type IndexReport struct {
	Indexed   int           `json:"indexed"`
	Unchanged int           `json:"unchanged"`
	Skipped   []SkippedFile `json:"skipped"`
	Symbols   int           `json:"symbols"`
	Edges     int           `json:"edges"`
	Chunks    int           `json:"chunks"`
}

// SkippedFile is one path the index build/update did not ingest.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// IndexBuild ingests the given workspace-relative paths into the session's
// code index. The kernel re-gates every path through FileRead policy, so
// denied paths come back in Skipped rather than entering the index.
func (s *Service) IndexBuild(sessionID string, paths []string) (*IndexReport, error) {
	var r IndexReport
	err := s.call("kernel.index.build", map[string]any{"session_id": sessionID, "paths": paths}, &r)
	return &r, err
}

// IndexUpdate invalidates and re-ingests changed paths (and drops deleted
// ones) after a patch apply/rollback.
func (s *Service) IndexUpdate(sessionID string, changed, deleted []string) (*IndexReport, error) {
	params := map[string]any{"session_id": sessionID, "changed_paths": changed}
	if len(deleted) > 0 {
		params["deleted_paths"] = deleted
	}
	var r IndexReport
	err := s.call("kernel.index.update", params, &r)
	return &r, err
}

// IndexSearch runs governed keyword search (FTS5 BM25 + exact, RRF-fused).
func (s *Service) IndexSearch(sessionID, query string, limit int) (json.RawMessage, error) {
	params := map[string]any{"session_id": sessionID, "query": query}
	if limit > 0 {
		params["limit"] = limit
	}
	var out json.RawMessage
	err := s.call("kernel.index.search", params, &out)
	return out, err
}

// IndexSymbols looks up definitions (and approximate references) by name.
func (s *Service) IndexSymbols(sessionID, name string, includeRefs bool) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call("kernel.index.symbols", map[string]any{
		"session_id": sessionID, "name": name, "include_refs": includeRefs,
	}, &out)
	return out, err
}

// IndexImpact walks the bounded transitive dependents of a symbol name
// (kernel.index.impact). maxDepth/limit <= 0 use the kernel defaults; the
// kernel clamps both into its documented bounds (1..=5, 1..=200).
func (s *Service) IndexImpact(sessionID, name string, maxDepth, limit int) (json.RawMessage, error) {
	params := map[string]any{"session_id": sessionID, "name": name}
	if maxDepth > 0 {
		params["max_depth"] = maxDepth
	}
	if limit > 0 {
		params["limit"] = limit
	}
	var out json.RawMessage
	err := s.call("kernel.index.impact", params, &out)
	return out, err
}

// IndexMap renders the PageRank-ranked repo map within a token budget.
func (s *Service) IndexMap(sessionID string, tokenBudget int) (json.RawMessage, error) {
	params := map[string]any{"session_id": sessionID}
	if tokenBudget > 0 {
		params["token_budget"] = tokenBudget
	}
	var out json.RawMessage
	err := s.call("kernel.index.map", params, &out)
	return out, err
}

// PendingChunk is one chunk lacking an embedding for a model
// (kernel.index.pending_chunks). Content is full chunk text — the kernel
// releases it only through the CodeIndex policy gate.
type PendingChunk struct {
	ChunkID     int64  `json:"chunk_id"`
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
}

// PendingChunksResult is one policy-gated batch of chunks to embed.
type PendingChunksResult struct {
	Chunks       []PendingChunk `json:"chunks"`
	TotalPending int            `json:"total_pending"`
}

// ChunkEmbedding is one caller-computed vector to store, echoing the
// content_hash returned by IndexPendingChunks.
type ChunkEmbedding struct {
	ChunkID     int64
	ContentHash string
	Vector      []float32
}

// EmbedStoreResult reports an embed_store outcome; Stale ids were replaced or
// deleted since the caller fetched them (expected under concurrent edits).
type EmbedStoreResult struct {
	Stored       int     `json:"stored"`
	Stale        []int64 `json:"stale"`
	TotalPending int     `json:"total_pending"`
}

// IndexPendingChunks returns up to limit chunks lacking an embedding for
// modelID (ascending chunk_id) plus the total backlog size.
func (s *Service) IndexPendingChunks(sessionID, modelID string, limit int) (*PendingChunksResult, error) {
	params := map[string]any{"session_id": sessionID, "model_id": modelID}
	if limit > 0 {
		params["limit"] = limit
	}
	var r PendingChunksResult
	err := s.call("kernel.index.pending_chunks", params, &r)
	return &r, err
}

// IndexEmbedStore stores caller-computed vectors (base64 f32-LE transport).
// The kernel never computes or fetches embeddings itself.
func (s *Service) IndexEmbedStore(sessionID, modelID string, dims int, items []ChunkEmbedding) (*EmbedStoreResult, error) {
	embeddings := make([]map[string]any, 0, len(items))
	for _, item := range items {
		embeddings = append(embeddings, map[string]any{
			"chunk_id":      item.ChunkID,
			"content_hash":  item.ContentHash,
			"vector_base64": encodeVectorBase64(item.Vector),
		})
	}
	var r EmbedStoreResult
	err := s.call("kernel.index.embed_store", map[string]any{
		"session_id": sessionID, "model_id": modelID, "dims": dims, "embeddings": embeddings,
	}, &r)
	return &r, err
}

// IndexSearchVector runs governed search with the additional cosine channel
// (three-way RRF). The existing IndexSearch stays the keyword-only surface.
func (s *Service) IndexSearchVector(sessionID, query string, limit int, modelID string, vector []float32) (json.RawMessage, error) {
	params := map[string]any{
		"session_id":          sessionID,
		"query":               query,
		"model_id":            modelID,
		"query_vector_base64": encodeVectorBase64(vector),
	}
	if limit > 0 {
		params["limit"] = limit
	}
	var out json.RawMessage
	err := s.call("kernel.index.search", params, &out)
	return out, err
}

// encodeVectorBase64 encodes a vector as base64 f32 little-endian
// (dims * 4 bytes), the kernel's BLOB layout.
func encodeVectorBase64(vector []float32) string {
	buf := make([]byte, 0, len(vector)*4)
	for _, v := range vector {
		buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(v))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// IndexEdge is one LSP-derived precise relation to persist
// (kernel.index.edges_store, docs/plans/code-intelligence.md V4 §B). Paths
// are workspace-relative; lines are 1-based.
type IndexEdge struct {
	SrcPath string
	SrcLine int
	DstPath string
	DstLine int
}

// SkippedEdge is one edge the kernel did not persist, with the reason
// (FileRead-denied endpoint, unresolvable symbol, self edge).
type SkippedEdge struct {
	SrcPath string `json:"src_path"`
	DstPath string `json:"dst_path"`
	Reason  string `json:"reason"`
}

// EdgesStoreResult reports a kernel.index.edges_store outcome; skips are
// expected under concurrent edits and are success, not error.
type EdgesStoreResult struct {
	Stored  int           `json:"stored"`
	Skipped []SkippedEdge `json:"skipped"`
}

// IndexEdgesStore persists LSP-sourced precise edges. The kernel re-gates
// both endpoints through FileRead policy and resolves them against indexed
// symbols — the edges store never ingests content.
func (s *Service) IndexEdgesStore(sessionID string, edges []IndexEdge) (*EdgesStoreResult, error) {
	payload := make([]map[string]any, 0, len(edges))
	for _, e := range edges {
		payload = append(payload, map[string]any{
			"src_path": e.SrcPath, "src_line": e.SrcLine,
			"dst_path": e.DstPath, "dst_line": e.DstLine,
		})
	}
	var r EdgesStoreResult
	err := s.call("kernel.index.edges_store", map[string]any{
		"session_id": sessionID, "edges": payload,
	}, &r)
	return &r, err
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
