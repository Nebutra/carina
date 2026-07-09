package daemon

// Governed code intelligence (docs/plans/code-intelligence.md): the agent
// tools code.search / code.symbols / code.map route to kernel.index.*. The
// daemon never opens the index database itself — its only view of the index
// is the kernel RPC surface, so the kernel stays the single governor (every
// query is a CodeIndex capability decision in the audit chain, and ingestion
// is re-gated per path by FileRead policy kernel-side).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/lsp"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// ensureIndex lazily builds the session's code index on first use. Candidate
// files come from the Zig scanner filtered to the supported languages; the
// list is a suggestion, never an authorization — the kernel re-gates every
// path through FileRead policy and skips what the session may not read.
// Freshness beyond the first build is maintained by the invalidation hooks
// (patch apply/rollback re-ingests the touched paths, and a mutating `run`
// command clears the built flag so the next code.* call re-syncs here,
// content-hash keyed) plus the mtime staleness sweep (V4): on the already-
// built branch a cheap (mtime, size, mode) diff against the last-sync
// snapshot routes out-of-band edits through kernel.index.update.
func (d *Daemon) ensureIndex(sess *sessionstore.Session, task *scheduler.Task) error {
	if _, built := d.indexBuilt.Load(sess.SessionID); built {
		if sweepEnabled() {
			d.sweepIndex(sess, task)
		}
		return nil
	}
	snap, err := d.scanSupportedStamps(sess.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("scan workspace: %w", err)
	}
	paths := make([]string, 0, len(snap.stamps))
	for p := range snap.stamps {
		paths = append(paths, p)
	}
	if _, err := d.kern.IndexBuild(sess.SessionID, paths); err != nil {
		return err
	}
	d.indexBuilt.Store(sess.SessionID, true)
	d.indexSnapshot.Store(sess.SessionID, snap)
	// Semantic layer (V2): drain the embedding backlog best-effort — an
	// embedding failure never fails the code.* tool that triggered the build,
	// but it is observable (V3): log line, audit event, daemon.status entry.
	if err := d.syncEmbeddings(sess.SessionID); err != nil {
		d.noteEmbeddingSyncFailure(sess.SessionID, task.TaskID, err)
	}
	return nil
}

// fileStamp is one sweep snapshot entry: file metadata only — the daemon
// never reads file bytes for the sweep (content is read, and re-gated per
// path through FileRead, exclusively kernel-side in kernel.index.update).
// mtime is nanoseconds: a Unix-second stamp hides a same-second, same-size
// out-of-band edit forever — the exact blind spot the sweep exists to close.
// mode carries the stat permission bits: a chmod flips readability without
// touching mtime or size (ctime only), and a file that was stat-able but
// unreadable at build time (never ingested) must still converge once
// `chmod +r` lands — the mode diff routes it through kernel.index.update,
// whose read now succeeds.
type fileStamp struct {
	mtime int64 // Unix nanoseconds
	size  int64
	mode  os.FileMode
}

// sweepSnapshot is a session's last-sync view: per-path stamps plus the time
// the stat pass began. scannedAt powers the racy-stamp rule (git's
// racy-clean treatment): a stamp whose mtime is not strictly older than the
// scan that captured it cannot prove the content unchanged — a write landing
// on the very same timestamp right after the stat would be invisible even at
// nanosecond precision — so the next sweep treats the path as changed
// (kernel.index.update is content-hash keyed, making the redundant update
// cheap).
type sweepSnapshot struct {
	stamps    map[string]fileStamp
	scannedAt int64 // UnixNano when the stat pass began
}

// sweepEnabled reports whether the mtime staleness sweep runs (default on;
// CARINA_INDEX_SWEEP=off|0|false opts perf-sensitive setups out).
func sweepEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CARINA_INDEX_SWEEP"))) {
	case "off", "0", "false":
		return false
	}
	return true
}

// scanSupportedStamps walks the workspace via the Zig scanner and returns
// path -> (mtime, size, mode) for the supported-language files. The scanner
// supplies the candidate list; its JSONL mtime field (Unix seconds, and the
// shipped binary emits none at all) is deliberately not consumed — a
// second-granularity stamp cannot carry a same-second, same-size edit — so
// one os.Stat per supported file captures nanosecond mtimes plus the mode
// bits instead. Still metadata only, never content.
func (d *Daemon) scanSupportedStamps(root string) (*sweepSnapshot, error) {
	files, err := d.tools.Scan(root)
	if err != nil {
		return nil, err
	}
	snap := &sweepSnapshot{stamps: make(map[string]fileStamp), scannedAt: time.Now().UnixNano()}
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f.Path)) {
		case ".rs", ".go", ".ts", ".tsx", ".py":
		default:
			continue
		}
		info, err := os.Stat(resolveIn(root, f.Path))
		if err != nil {
			continue // vanished mid-scan: the next sweep sees the delete
		}
		snap.stamps[f.Path] = fileStamp{mtime: info.ModTime().UnixNano(), size: info.Size(), mode: info.Mode()}
	}
	return snap, nil
}

// sweepIndex converges the built index to what is on disk and readable (V4
// full-sync semantics): changed stamps (mtime, size, or mode — a chmod +r
// must converge a file whose build-time read failed) and added paths route
// through kernel.index.update (which re-gates FileRead per path and drops on
// deny/NotFound), vanished paths become deletes. An empty diff makes no
// kernel call. Best-effort: a failure never fails the calling code.* tool,
// but it surfaces (log + index_sweep_failed audit event) and clears
// indexBuilt so the next call heals with a full build.
func (d *Daemon) sweepIndex(sess *sessionstore.Session, task *scheduler.Task) {
	cur, err := d.scanSupportedStamps(sess.WorkspaceRoot)
	if err != nil {
		d.noteSweepFailure(sess.SessionID, task.TaskID, "scan-error", err)
		return
	}
	prev := &sweepSnapshot{stamps: map[string]fileStamp{}}
	if v, ok := d.indexSnapshot.Load(sess.SessionID); ok {
		prev = v.(*sweepSnapshot)
	}
	var changed, deleted []string
	for p, st := range cur.stamps {
		// Changed on any stamp difference — or when the snapshot stamp is racy
		// (mtime not strictly older than the stat pass that captured it): such
		// a stamp cannot prove the content unchanged, so re-send the path.
		if old, ok := prev.stamps[p]; !ok || old != st || old.mtime >= prev.scannedAt {
			changed = append(changed, p)
		}
	}
	for p := range prev.stamps {
		if _, ok := cur.stamps[p]; !ok {
			deleted = append(deleted, p)
		}
	}
	if len(changed) == 0 && len(deleted) == 0 {
		return
	}
	if _, err := d.kern.IndexUpdate(sess.SessionID, changed, deleted); err != nil {
		d.noteSweepFailure(sess.SessionID, task.TaskID, "kernel-error", err)
		return
	}
	if err := d.syncEmbeddings(sess.SessionID); err != nil {
		d.noteEmbeddingSyncFailure(sess.SessionID, task.TaskID, err)
	}
	d.indexSnapshot.Store(sess.SessionID, cur)
}

// noteSweepFailure surfaces a failed staleness sweep (V4): one daemon log
// line, an index_sweep_failed audit event (classified reason only, never
// content), and a cleared indexBuilt flag so the next code.* call heals with
// a full build.
func (d *Daemon) noteSweepFailure(sessionID, taskID, reason string, err error) {
	fmt.Printf("carina-daemon: index sweep failed (session %s): %v\n", sessionID, err)
	d.record(sessionID, "TaskCreated", taskID, "go",
		map[string]any{"status": "index_sweep_failed", "reason": reason}, "")
	d.indexBuilt.Delete(sessionID)
	d.indexSnapshot.Delete(sessionID)
}

// invalidateIndex keeps the code index in step with a write (patch apply or
// rollback). Best-effort: an index error never fails the write, and it only
// runs once the session has built an index — the kernel-side hook covers
// every other case. A failure is no longer swallowed (V4 D4): one daemon log
// line, an index_invalidation_failed audit event (classified reason only,
// never content), and a cleared indexBuilt flag so the next code.* call
// heals with a full build (full-sync semantics — a stale index is visible,
// bounded, and self-healing).
func (d *Daemon) invalidateIndex(sessionID string, changed []string) {
	if _, built := d.indexBuilt.Load(sessionID); !built {
		return
	}
	if _, err := d.kern.IndexUpdate(sessionID, changed, nil); err != nil {
		fmt.Printf("carina-daemon: index invalidation failed (session %s): %v\n", sessionID, err)
		d.record(sessionID, "TaskCreated", "", "go",
			map[string]any{"status": "index_invalidation_failed", "reason": "kernel-error"}, "")
		d.indexBuilt.Delete(sessionID)
		d.indexSnapshot.Delete(sessionID)
		return
	}
	// Best-effort re-embed of changed chunks — failures never fail the write,
	// but they surface (V3): log line, audit event, daemon.status entry.
	if err := d.syncEmbeddings(sessionID); err != nil {
		d.noteEmbeddingSyncFailure(sessionID, "", err)
	}
}

// codeIntelError folds a kernel.index.* error into an agent observation,
// surfacing policy denials (and approval escalations, which carry the
// decision_id for the control plane) in the same DENIED form as every other
// tool.
func codeIntelError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "code index denied") || strings.Contains(msg, "code index requires approval") {
		return "DENIED: " + msg
	}
	return "code intel error: " + msg
}

// searchHit is one rendered code.search result row (kernel.index.search hit).
type searchHit struct {
	Path      string  `json:"path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
	Symbol    *struct {
		QualifiedName string `json:"qualified_name"`
		Kind          string `json:"kind"`
	} `json:"symbol"`
}

// agentCodeSearch renders ranked search (BM25 + exact + optional cosine,
// RRF-fused) under a first-line header stating the channels actually used
// for this query (V3 observable degrade); a semantic-channel degrade also
// lands in the audit chain as a reason, never content.
func (d *Daemon) agentCodeSearch(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	if strings.TrimSpace(act.Query) == "" {
		return "error: code.search needs a query"
	}
	if err := d.ensureIndex(sess, task); err != nil {
		return codeIntelError(err)
	}
	raw, semReason, err := d.searchIndexDegraded(sess.SessionID, act.Query, 10)
	if err != nil {
		return codeIntelError(err)
	}
	var res struct {
		Hits []searchHit `json:"hits"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "code intel error: " + err.Error()
	}
	header := "channels: keyword:on semantic:on"
	if semReason != "" {
		header = "channels: keyword:on semantic:off(" + semReason + ")"
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
			map[string]any{"status": "code_search_degraded", "semantic": "off", "reason": semReason}, "")
	}
	hits := res.Hits
	rr, rrReason := d.rerankModelSelection()
	if rrReason != "" {
		// A configured-but-unavailable selection degrades observably (V4 §C):
		// header segment + audit reason, and snippets go nowhere.
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
			map[string]any{"status": "code_rerank_degraded", "reason": rrReason}, "")
		header += " rerank:off(" + rrReason + ")"
	} else if rr != nil && len(hits) > 0 {
		hits, header = d.rerankHits(sess, task, rr, act.Query, hits, header)
	}
	if len(hits) == 0 {
		return header + "\nno matches"
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "%s:%d-%d", h.Path, h.StartLine, h.EndLine)
		if h.Symbol != nil {
			fmt.Fprintf(&b, "  (%s %s)", h.Symbol.Kind, h.Symbol.QualifiedName)
		}
		b.WriteString("\n")
		b.WriteString(truncate(h.Snippet, 300))
		b.WriteString("\n---\n")
	}
	return strings.TrimSpace(b.String())
}

// rerankHits applies the optional rerank stage (V3 §C) to governed search
// hits: a valid permutation reorders them and the header names the reranker;
// any failure (deadline, error, or invalid permutation) falls back to the
// kernel order with rerank:off(rerank-error) and a code_rerank_degraded
// audit event. The call runs under rerankTimeout (V4 §C) so a hanging
// provider degrades instead of stalling the tool.
func (d *Daemon) rerankHits(sess *sessionstore.Session, task *scheduler.Task, rr Reranker, query string, hits []searchHit, header string) ([]searchHit, string) {
	cands := make([]rerankCandidate, len(hits))
	for i, h := range hits {
		cands[i] = rerankCandidate{Path: h.Path, StartLine: h.StartLine, EndLine: h.EndLine, Snippet: h.Snippet, Score: h.Score}
	}
	ctx, cancel := context.WithTimeout(context.Background(), rerankTimeout)
	defer cancel()
	perm, err := rr.Rerank(ctx, query, cands)
	if err != nil || !validPermutation(perm, len(hits)) {
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
			map[string]any{"status": "code_rerank_degraded", "reranker": rr.Name(), "reason": "rerank-error"}, "")
		return hits, header + " rerank:off(rerank-error)"
	}
	out := make([]searchHit, len(hits))
	for i, p := range perm {
		out[i] = hits[p]
	}
	return out, header + " rerank:" + rr.Name()
}

// agentCodeSymbols renders definitions plus approximate references.
func (d *Daemon) agentCodeSymbols(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	if strings.TrimSpace(act.Name) == "" {
		return "error: code.symbols needs a name"
	}
	if err := d.ensureIndex(sess, task); err != nil {
		return codeIntelError(err)
	}
	res, errObs := d.lookupSymbols(sess, act.Name)
	if errObs != "" {
		return errObs
	}
	if len(res.Definitions) == 0 && len(res.References) == 0 {
		return "no symbol named " + act.Name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "definitions (%d):\n", len(res.Definitions))
	for _, def := range res.Definitions {
		fmt.Fprintf(&b, "  %s %s  %s:%d-%d\n", def.Kind, def.QualifiedName, def.Path, def.StartLine, def.EndLine)
	}
	fmt.Fprintf(&b, "references (%d, confidence=%s):\n", len(res.References), res.Confidence)
	for i, ref := range res.References {
		if i >= 20 {
			fmt.Fprintf(&b, "  … %d more\n", len(res.References)-i)
			break
		}
		fmt.Fprintf(&b, "  %s:%d: %s\n", ref.Path, ref.Line, truncate(ref.Text, 120))
	}
	return strings.TrimSpace(b.String())
}

// symbolsResult is the kernel.index.symbols response shared by code.symbols
// and the code.def / code.refs tree-sitter fallback.
type symbolsResult struct {
	Definitions []symbolDefinition `json:"definitions"`
	References  []symbolReference  `json:"references"`
	Confidence  string             `json:"confidence"`
}

type symbolDefinition struct {
	Path          string `json:"path"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
}

type symbolReference struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// lookupSymbols runs the governed kernel symbols lookup — the policy gate for
// every by-name code tool. A non-empty second return is the error observation.
func (d *Daemon) lookupSymbols(sess *sessionstore.Session, name string) (*symbolsResult, string) {
	raw, err := d.kern.IndexSymbols(sess.SessionID, name, true)
	if err != nil {
		return nil, codeIntelError(err)
	}
	var res symbolsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, "code intel error: " + err.Error()
	}
	return &res, ""
}

// agentCodeDef renders a symbol's definition sites: LSP-precise when a
// language server is available (confidence=lsp), else the kernel's
// tree-sitter definitions with their honest confidence.
func (d *Daemon) agentCodeDef(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	return d.agentSemanticLookup(sess, task, act, "code.def")
}

// agentCodeRefs renders a symbol's reference sites, LSP-first like code.def.
func (d *Daemon) agentCodeRefs(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	return d.agentSemanticLookup(sess, task, act, "code.refs")
}

func (d *Daemon) agentSemanticLookup(sess *sessionstore.Session, task *scheduler.Task, act *action, tool string) string {
	if strings.TrimSpace(act.Name) == "" {
		return "error: " + tool + " needs a name"
	}
	if err := d.ensureIndex(sess, task); err != nil {
		return codeIntelError(err)
	}
	// The kernel lookup is the CodeIndex policy gate: a denial returns here,
	// before any language server runs.
	res, errObs := d.lookupSymbols(sess, act.Name)
	if errObs != "" {
		return errObs
	}
	if len(res.Definitions) == 0 && len(res.References) == 0 {
		return "no symbol named " + act.Name
	}
	wantDef := tool == "code.def"
	locs, degradeReason := d.lspLocations(sess, task, act.Name, res, wantDef)
	if degradeReason == "" {
		// Opportunistic write-through (V4 §B): the LSP path answered, so the
		// locations are precise symbol->symbol relations worth persisting as
		// source='lsp' edges. Best-effort — an error never fails the tool.
		d.persistLSPEdges(sess, res.Definitions[0], locs, wantDef)
		var b strings.Builder
		section := "references"
		if wantDef {
			section = "definitions"
		}
		fmt.Fprintf(&b, "%s (%d, precision:lsp):\n", section, len(locs))
		for i, loc := range locs {
			if i >= 20 {
				fmt.Fprintf(&b, "  … %d more\n", len(locs)-i)
				break
			}
			fmt.Fprintf(&b, "  %s:%d:%d\n", loc.Path, loc.Line, loc.Char)
		}
		return strings.TrimSpace(b.String())
	}
	// Tree-sitter fallback: the V1 approximate results, honestly labeled with
	// the degrade reason — which also lands in the audit chain (reason only).
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{
		"status": "code_lookup_degraded", "tool": tool, "precision": "tree-sitter", "reason": degradeReason}, "")
	precision := fmt.Sprintf("precision:tree-sitter(%s)", degradeReason)
	var b strings.Builder
	if wantDef {
		fmt.Fprintf(&b, "definitions (%d, %s):\n", len(res.Definitions), precision)
		for _, def := range res.Definitions {
			fmt.Fprintf(&b, "  %s %s  %s:%d-%d\n", def.Kind, def.QualifiedName, def.Path, def.StartLine, def.EndLine)
		}
	} else {
		fmt.Fprintf(&b, "references (%d, %s):\n", len(res.References), precision)
		for i, ref := range res.References {
			if i >= 20 {
				fmt.Fprintf(&b, "  … %d more\n", len(res.References)-i)
				break
			}
			fmt.Fprintf(&b, "  %s:%d: %s\n", ref.Path, ref.Line, truncate(ref.Text, 120))
		}
	}
	return strings.TrimSpace(b.String())
}

// maxLSPEdgesPerStore caps one write-through batch, mirroring the kernel's
// per-call edges_store bound.
const maxLSPEdgesPerStore = 256

// persistLSPEdges opportunistically persists LSP-precise relations as
// source='lsp' edges via kernel.index.edges_store (V4 §B) — write-through
// only, never a workspace crawl: it runs exactly when code.def / code.refs
// already obtained governed LSP locations. For code.refs each location is a
// precise reference to the anchor definition (src=location, dst=anchor); for
// code.def only locations outside the anchor's own span persist (src=anchor,
// dst=location — the anchor resolves there, e.g. an alias/re-export).
// Best-effort: an error never fails the tool (one log line — the gated
// kernel call itself is already in the audit chain). The kernel re-gates
// FileRead per endpoint and skips unresolvable/self edges.
func (d *Daemon) persistLSPEdges(sess *sessionstore.Session, anchor symbolDefinition, locs []lsp.Location, wantDef bool) {
	canonRoot := canonicalPath(sess.WorkspaceRoot)
	edges := make([]kernel.IndexEdge, 0, len(locs))
	for _, loc := range locs {
		rel, err := filepath.Rel(canonRoot, loc.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue // already workspace-filtered; never hand the kernel a stray
		}
		rel = filepath.ToSlash(rel)
		onAnchor := rel == anchor.Path && loc.Line >= anchor.StartLine && loc.Line <= anchor.EndLine
		if wantDef {
			if onAnchor {
				continue // the anchor position itself: a self relation
			}
			edges = append(edges, kernel.IndexEdge{
				SrcPath: anchor.Path, SrcLine: anchor.StartLine,
				DstPath: rel, DstLine: loc.Line,
			})
		} else {
			edges = append(edges, kernel.IndexEdge{
				SrcPath: rel, SrcLine: loc.Line,
				DstPath: anchor.Path, DstLine: anchor.StartLine,
			})
		}
		if len(edges) >= maxLSPEdgesPerStore {
			break
		}
	}
	if len(edges) == 0 {
		return
	}
	if _, err := d.kern.IndexEdgesStore(sess.SessionID, edges); err != nil {
		fmt.Printf("carina-daemon: lsp edge write-through failed (session %s): %v\n", sess.SessionID, err)
	}
}

// lspQueryTimeout bounds one language-server handshake or query.
const lspQueryTimeout = 8 * time.Second

// credentialEnvVar matches credential-bearing environment variable names
// (provider keys, tokens, secrets): a language server never needs them.
func credentialEnvVar(name string) bool {
	up := strings.ToUpper(name)
	for _, marker := range []string{"API_KEY", "APIKEY", "_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIAL", "ACCESS_KEY"} {
		if strings.Contains(up, marker) {
			return true
		}
	}
	return false
}

// lspEnv is the governed environment for language-server children: the
// daemon environment minus credential-bearing variables, plus the egress
// proxy overrides. LSP servers are agent-triggerable children that run
// outside the sandboxed command path, so they must never inherit daemon
// secrets, and their network I/O flows through the same governed egress
// chokepoint as agent command children (audited, policy-evaluated) instead
// of an ungoverned direct path.
func (d *Daemon) lspEnv() []string {
	base := os.Environ()
	env := make([]string, 0, len(base))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if credentialEnvVar(name) {
			continue
		}
		env = append(env, kv)
	}
	// Later entries win (exec.Cmd deduplicates keeping the last), so the
	// egress overrides apply even when the variables were already set.
	return append(env, d.egressEnv()...)
}

// lspLocations tries the precise LSP path for code.def / code.refs: start a
// server for the definition's file type, open the file, and query at the
// tree-sitter definition position. The second return is the degrade reason
// ("" = the LSP path answered): "read-denied" (the anchor resolves outside
// the workspace or the kernel FileRead gate denied it), "no-boundary-match"
// (the indexed position no longer contains the symbol), "lsp-unavailable"
// (everything else: no server for the extension, binary absent,
// handshake/query failure, everything filtered) — the caller degrades to
// tree-sitter and records the reason. LSP reads stay workspace-scoped
// (symlink-canonicalized containment) and pass the kernel FileRead gate
// (audited) before any content reaches a language server process. Wire
// columns are UTF-16 code units: the outbound query converts the byte
// column, and returned locations convert back to rune columns for rendering.
func (d *Daemon) lspLocations(sess *sessionstore.Session, task *scheduler.Task, name string, res *symbolsResult, wantDef bool) ([]lsp.Location, string) {
	if len(res.Definitions) == 0 {
		return nil, "lsp-unavailable"
	}
	root := sess.WorkspaceRoot
	def := res.Definitions[0]
	srv, ok := lspServerForExt(strings.ToLower(filepath.Ext(def.Path)))
	if !ok {
		return nil, "lsp-unavailable"
	}
	abs := filepath.Clean(resolveIn(root, def.Path))
	canonAbs := canonicalPath(abs)
	if !strings.HasPrefix(canonAbs, canonicalPath(root)+string(filepath.Separator)) {
		// The anchor resolves outside the workspace (e.g. a symlink swapped
		// in after indexing): never hand the server that path or content.
		return nil, "read-denied"
	}
	dec, err := d.kern.Request(sess.SessionID, "FileRead", abs, task.TaskID)
	if err != nil || dec.Decision != "allowed" {
		return nil, "read-denied" // the denial is in the audit chain; fall back
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, "lsp-unavailable"
	}
	d.record(sess.SessionID, "FileRead", task.TaskID, "go",
		map[string]any{"path": abs, "bytes": len(content), "purpose": "lsp"}, dec.DecisionID)
	line, char, ok := findNamePosition(string(content), def.StartLine, name)
	if !ok {
		return nil, "no-boundary-match"
	}
	// Outbound position: LSP wire columns are UTF-16 code units (D2).
	anchorLines := strings.Split(string(content), "\n")
	utf16Char := char
	if line-1 >= 0 && line-1 < len(anchorLines) {
		utf16Char = lsp.UTF16Col(anchorLines[line-1], char-1) + 1
	}
	session, err := lsp.StartSession(srv.bin, srv.args, root, lspQueryTimeout, d.lspEnv())
	if err != nil {
		return nil, "lsp-unavailable"
	}
	defer session.Close()
	if err := session.DidOpen(abs, srv.langID, string(content)); err != nil {
		return nil, "lsp-unavailable"
	}
	var locs []lsp.Location
	if wantDef {
		locs, err = session.Definition(abs, line, utf16Char)
	} else {
		locs, err = session.References(abs, line, utf16Char, false)
	}
	if err != nil {
		return nil, "lsp-unavailable"
	}
	locs = filterWorkspaceLocations(root, locs)
	if len(locs) == 0 {
		return nil, "lsp-unavailable"
	}
	d.convertLocationColumns(sess, task, locs, canonAbs, anchorLines)
	return locs, ""
}

// renderedLocationCap mirrors the render loop's 20-location cap: only
// locations that can actually render convert columns (bounding the gated
// reads the conversion may need).
const renderedLocationCap = 20

// convertLocationColumns converts returned LSP columns (UTF-16 code units)
// to 1-based rune columns for rendering (D2). The anchor file's lines are
// already in hand; any other workspace file gets one kernel-FileRead-gated
// read, cached per call. On read denial/failure — or an out-of-range line —
// the location keeps the raw UTF-16 column rather than being dropped.
func (d *Daemon) convertLocationColumns(sess *sessionstore.Session, task *scheduler.Task, locs []lsp.Location, canonAnchor string, anchorLines []string) {
	cache := map[string][]string{canonAnchor: anchorLines}
	for i := range locs {
		if i >= renderedLocationCap {
			return
		}
		lines, ok := cache[locs[i].Path]
		if !ok {
			lines = d.readLinesGated(sess, task, locs[i].Path)
			cache[locs[i].Path] = lines
		}
		ln := locs[i].Line - 1
		if lines == nil || ln < 0 || ln >= len(lines) {
			continue
		}
		byteCol := lsp.ByteCol(lines[ln], locs[i].Char-1)
		locs[i].Char = utf8.RuneCountInString(lines[ln][:byteCol]) + 1
	}
}

// readLinesGated reads one workspace file through the kernel FileRead gate
// for column conversion; nil on denial or read failure (the caller keeps the
// raw column — never a reason to drop a location).
func (d *Daemon) readLinesGated(sess *sessionstore.Session, task *scheduler.Task, path string) []string {
	dec, err := d.kern.Request(sess.SessionID, "FileRead", path, task.TaskID)
	if err != nil || dec.Decision != "allowed" {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	d.record(sess.SessionID, "FileRead", task.TaskID, "go",
		map[string]any{"path": path, "bytes": len(content), "purpose": "lsp-render"}, dec.DecisionID)
	return strings.Split(string(content), "\n")
}

// findNamePosition locates name on (or just after) the 1-based startLine,
// returning a 1-based line/char position for LSP queries. Only identifier-
// boundary occurrences count: a first-substring match could sit inside an
// earlier, longer identifier (receiver type TaskList vs method Task) and
// aim the LSP query at the wrong symbol.
func findNamePosition(content string, startLine int, name string) (int, int, bool) {
	lines := strings.Split(content, "\n")
	for i := startLine - 1; i < len(lines) && i < startLine+2; i++ {
		if i < 0 {
			continue
		}
		if col := indexIdentifier(lines[i], name); col >= 0 {
			return i + 1, col + 1, true
		}
	}
	return 0, 0, false
}

// indexIdentifier returns the first occurrence of name in line that is not
// embedded in a longer identifier, or -1.
func indexIdentifier(line, name string) int {
	if name == "" {
		return -1
	}
	for from := 0; from+len(name) <= len(line); {
		col := strings.Index(line[from:], name)
		if col < 0 {
			return -1
		}
		col += from
		end := col + len(name)
		if (col == 0 || !isIdentByte(line[col-1])) && (end == len(line) || !isIdentByte(line[end])) {
			return col
		}
		from = col + 1
	}
	return -1
}

func isIdentByte(c byte) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// codeIntelStatus is the per-session semantic-layer health record surfaced
// on daemon.status as the code_intel map (V3 observable degrade): reasons
// and timestamps only, never content.
type codeIntelStatus struct {
	ModelID        string    // "" = semantic layer off
	EmbedDims      int       // dims of the last successful embed_store batch
	LastSyncAt     time.Time // when syncEmbeddings last ran for the session
	LastSyncError  string    // "" = healthy; error text, log/status only
	LastSyncReason string    // classified: provider-error|dims-mismatch|kernel-error
}

// lastEmbedDims is the dims of the session's last successful embed_store
// batch (0 = none recorded) — the daemon-side dims-mismatch reference.
func (d *Daemon) lastEmbedDims(sessionID string) int {
	if v, ok := d.codeIntelStatus.Load(sessionID); ok {
		return v.(codeIntelStatus).EmbedDims
	}
	return 0
}

// noteEmbedStoreSuccess records a healthy embed_store batch: the dims become
// the session's dims-mismatch reference and the status entry reads healthy.
func (d *Daemon) noteEmbedStoreSuccess(sessionID, modelID string, dims int) {
	d.codeIntelStatus.Store(sessionID, codeIntelStatus{
		ModelID: modelID, EmbedDims: dims, LastSyncAt: time.Now().UTC(),
	})
}

// noteEmbedSyncHealthy records a fully drained sync pass: error fields clear
// (a recovered provider heals observably) while the dims reference survives.
func (d *Daemon) noteEmbedSyncHealthy(sessionID, modelID string) {
	st := codeIntelStatus{}
	if v, ok := d.codeIntelStatus.Load(sessionID); ok {
		st = v.(codeIntelStatus)
	}
	st.ModelID = modelID
	st.LastSyncAt = time.Now().UTC()
	st.LastSyncError = ""
	st.LastSyncReason = ""
	d.codeIntelStatus.Store(sessionID, st)
}

// embedSyncHealthy reports whether the session's last embedding sync pass
// completed cleanly. False also when no sync ever ran in this process (a
// fresh daemon over an existing index): the caller re-syncs — cheap when the
// backlog is empty — before claiming the semantic channel.
func (d *Daemon) embedSyncHealthy(sessionID string) bool {
	if v, ok := d.codeIntelStatus.Load(sessionID); ok {
		return v.(codeIntelStatus).LastSyncError == ""
	}
	return false
}

// noteEmbeddingSyncFailure surfaces a failed syncEmbeddings pass on all three
// V3 observability surfaces — one daemon log line, an embedding_sync_failed
// audit event (classified reason only, never content), and the per-session
// daemon.status code_intel entry. The calling tool has already succeeded.
func (d *Daemon) noteEmbeddingSyncFailure(sessionID, taskID string, err error) {
	reason := syncFailureReason(err)
	fmt.Printf("carina-daemon: embedding sync failed (session %s): %v\n", sessionID, err)
	d.record(sessionID, "TaskCreated", taskID, "go",
		map[string]any{"status": "embedding_sync_failed", "reason": reason}, "")
	st := codeIntelStatus{ModelID: d.embeddingsModelID()}
	if v, ok := d.codeIntelStatus.Load(sessionID); ok {
		st = v.(codeIntelStatus)
	}
	st.LastSyncAt = time.Now().UTC()
	st.LastSyncError = err.Error()
	st.LastSyncReason = reason
	d.codeIntelStatus.Store(sessionID, st)
}

// codeIntelStatusSnapshot renders the code_intel map for daemon.status:
// bounded (sessions that ran an embedding sync only), reasons and timestamps
// only — never chunk content or queries.
func (d *Daemon) codeIntelStatusSnapshot() map[string]any {
	out := map[string]any{}
	d.codeIntelStatus.Range(func(k, v any) bool {
		st := v.(codeIntelStatus)
		semantic := "on"
		if st.ModelID == "" || st.LastSyncError != "" {
			semantic = "off"
		}
		out[k.(string)] = map[string]any{
			"semantic":        semantic,
			"reason":          st.LastSyncReason,
			"model_id":        st.ModelID,
			"embed_dims":      st.EmbedDims,
			"last_sync_error": st.LastSyncError,
			"last_sync_at":    st.LastSyncAt.Format(time.RFC3339),
		}
		return true
	})
	return out
}

// impactResult is the kernel.index.impact response.
type impactResult struct {
	Seeds      []symbolDefinition `json:"seeds"`
	Dependents []struct {
		Symbol     symbolDefinition `json:"symbol"`
		Depth      int              `json:"depth"`
		Confidence float64          `json:"confidence"`
		Source     string           `json:"source"`
	} `json:"dependents"`
	Truncated bool `json:"truncated"`
}

// agentCodeImpact renders the dependency-impact report for a symbol name
// (V3): bounded transitive dependents over the tree-sitter edges graph,
// grouped by confidence tier (high >= 0.5, medium >= 0.1, low below). The
// kernel defaults bound the walk (depth<=3, limit 50); denials surface as
// DENIED like every code.* tool.
func (d *Daemon) agentCodeImpact(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	if strings.TrimSpace(act.Name) == "" {
		return "error: code.impact needs a name"
	}
	if err := d.ensureIndex(sess, task); err != nil {
		return codeIntelError(err)
	}
	raw, err := d.kern.IndexImpact(sess.SessionID, act.Name, 0, 0)
	if err != nil {
		return codeIntelError(err)
	}
	var res impactResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "code intel error: " + err.Error()
	}
	if len(res.Seeds) == 0 {
		return "no symbol named " + act.Name
	}
	// The header source is honest about precision (V4 §B): "lsp" only when
	// every dependent came through an all-lsp path; any approximate hop keeps
	// the tree-sitter label, and lsp-precise dependents are then annotated
	// per line.
	headerSource := "tree-sitter"
	if len(res.Dependents) > 0 {
		headerSource = "lsp"
		for _, dep := range res.Dependents {
			if dep.Source != "lsp" {
				headerSource = "tree-sitter"
				break
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "impact of %s (source: %s, depth<=3): %d dependents", act.Name, headerSource, len(res.Dependents))
	if res.Truncated {
		b.WriteString(" (truncated)")
	}
	b.WriteString("\n")
	if len(res.Dependents) == 0 {
		b.WriteString("no dependents within the walked depth")
		return strings.TrimSpace(b.String())
	}
	// Bucket by confidence tier, preserving the kernel's deterministic
	// (depth, confidence desc, path, id) order within each tier.
	tierOf := func(confidence float64) int {
		switch {
		case confidence >= 0.5:
			return 0 // high
		case confidence >= 0.1:
			return 1 // medium
		default:
			return 2 // low
		}
	}
	labels := [3]string{"high", "medium", "low"}
	for tier := 0; tier < 3; tier++ {
		wrote := false
		for _, dep := range res.Dependents {
			if tierOf(dep.Confidence) != tier {
				continue
			}
			if !wrote {
				fmt.Fprintf(&b, "%s:\n", labels[tier])
				wrote = true
			}
			srcNote := ""
			if dep.Source == "lsp" && headerSource != "lsp" {
				srcNote = ", source: lsp"
			}
			fmt.Fprintf(&b, "  %s:%d-%d  %s %s  (depth %d, confidence %.2f%s)\n",
				dep.Symbol.Path, dep.Symbol.StartLine, dep.Symbol.EndLine,
				dep.Symbol.Kind, dep.Symbol.QualifiedName, dep.Depth, dep.Confidence, srcNote)
		}
	}
	return strings.TrimSpace(b.String())
}

// agentCodeMap renders the PageRank-ranked repo map within a token budget.
func (d *Daemon) agentCodeMap(sess *sessionstore.Session, task *scheduler.Task, act *action) string {
	if err := d.ensureIndex(sess, task); err != nil {
		return codeIntelError(err)
	}
	raw, err := d.kern.IndexMap(sess.SessionID, 1024)
	if err != nil {
		return codeIntelError(err)
	}
	var res struct {
		Map           string `json:"map"`
		TokenEstimate int    `json:"token_estimate"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "code intel error: " + err.Error()
	}
	if strings.TrimSpace(res.Map) == "" {
		return "repo map is empty (no indexed symbols)"
	}
	return res.Map
}
