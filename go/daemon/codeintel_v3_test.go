package daemon

// V3 tests (docs/plans/code-intelligence.md): the code.impact tool, the
// observable-degrade surfaces (result headers, degrade-reason audit events,
// the daemon.status code_intel map, the sync-failure log line), the reranker
// seam (no-op default, fake reorder, fallback on error/invalid permutation),
// and the V2 deferred minors — UTF-16 LSP columns (D2), percent-encoded URIs
// plus symlink-canonicalized containment (D3), and dims-mismatch embedding
// degrade (D4).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/lsp"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// v3FakeEmbedder is a dims-configurable embeddings fake: it can fail hard,
// return ragged (wrong-dims) batches, return zero-length vectors, or switch
// dims between calls — every degrade the V3 tests need.
type v3FakeEmbedder struct {
	name      string
	dims      int
	fail      bool
	failMulti bool // fail batch (multi-input) requests only; query embeds succeed
	ragged    bool // odd-indexed vectors get dims+1 components (wrong-dims batch)
	zero      bool // every vector is zero-length
	calls     int
}

func (f *v3FakeEmbedder) Name() string { return f.name }

func (f *v3FakeEmbedder) Embed(_ context.Context, req modelrouter.EmbeddingsRequest) (*modelrouter.EmbeddingsResponse, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("fake embedder down")
	}
	if f.failMulti && len(req.Inputs) > 1 {
		return nil, errors.New("fake embedder rejects batches")
	}
	vectors := make([][]float32, len(req.Inputs))
	for i := range req.Inputs {
		switch {
		case f.zero:
			vectors[i] = []float32{}
		case f.ragged && i%2 == 1:
			vectors[i] = make([]float32, f.dims+1)
			vectors[i][0] = 1
		default:
			vectors[i] = make([]float32, f.dims)
			vectors[i][0] = 1
		}
	}
	model := req.Model
	if model == "" || model == "default" {
		model = "unit-embed-model"
	}
	return &modelrouter.EmbeddingsResponse{
		Provider: f.name, Model: model, Vectors: vectors, InputTokens: len(req.Inputs),
	}, nil
}

// v3StatusEventPayloads returns the payloads of every audit event whose
// payload.status matches (the daemon degrade-event idiom: reasons, never
// content).
func v3StatusEventPayloads(t *testing.T, d *Daemon, sessionID, status string) []map[string]any {
	t.Helper()
	raw, err := d.kern.ReadEvents(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var evs []map[string]any
	if err := json.Unmarshal(raw, &evs); err != nil {
		t.Fatalf("unmarshal events: %v", err)
	}
	var out []map[string]any
	for _, e := range evs {
		if p, ok := e["payload"].(map[string]any); ok && p["status"] == status {
			out = append(out, p)
		}
	}
	return out
}

// v3CodeIntelStatus fetches the daemon.status code_intel entry for a session.
func v3CodeIntelStatus(t *testing.T, d *Daemon, sessionID string) map[string]any {
	t.Helper()
	res, err := d.handleStatus(nil)
	if err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	var st struct {
		CodeIntel map[string]map[string]any `json:"code_intel"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if st.CodeIntel == nil {
		t.Fatalf("daemon.status must expose a code_intel map, got: %s", b)
	}
	entry, ok := st.CodeIntel[sessionID]
	if !ok {
		t.Fatalf("daemon.status code_intel has no entry for session %s: %s", sessionID, b)
	}
	return entry
}

// v3CaptureStdout captures fmt.Printf daemon log lines emitted during fn.
func v3CaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

// v3HitPaths extracts the rendered code.search hit paths in order.
var v3HitPathRe = regexp.MustCompile(`(?m)^([^\s:]+\.rs):\d+-\d+`)

func v3HitPaths(obs string) []string {
	var out []string
	for _, m := range v3HitPathRe.FindAllStringSubmatch(obs, -1) {
		out = append(out, m[1])
	}
	return out
}

func v3FirstLine(obs string) string {
	if i := strings.IndexByte(obs, '\n'); i >= 0 {
		return obs[:i]
	}
	return obs
}

// ---- A. code.impact ---------------------------------------------------------

// TestCodeImpactIsReadOnlyAndDocumented: the tool is batchable, listed in the
// shared tool reference, and briefed with its symbol name.
func TestCodeImpactIsReadOnlyAndDocumented(t *testing.T) {
	if !isReadOnlyTool("code.impact") {
		t.Fatal("code.impact is a pure query and must be read-only (batchable)")
	}
	if !strings.Contains(toolsHelp, `"tool":"code.impact"`) {
		t.Fatal("toolsHelp must document code.impact")
	}
	if got := briefAction(&action{Tool: "code.impact", Name: "Kernel"}); got != "code.impact Kernel" {
		t.Fatalf("briefAction = %q, want %q", got, "code.impact Kernel")
	}
}

// TestCodeImpactRendersConfidenceTiers: the report groups bounded transitive
// dependents by confidence tier under the honest header, one line per
// dependent with depth and confidence.
func TestCodeImpactRendersConfidenceTiers(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "h.rs"), []byte("pub fn zz_v3imp_target() {}\n"), 0o600)
	for _, f := range []string{"c1.rs", "c2.rs", "c3.rs"} {
		os.WriteFile(filepath.Join(ws, f),
			[]byte("pub fn zz_v3imp_caller() {\n    zz_v3imp_target();\n}\n"), 0o600)
	}
	os.WriteFile(filepath.Join(ws, "o.rs"),
		[]byte("pub fn zz_v3imp_outer() {\n    zz_v3imp_caller();\n}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.impact", Name: "zz_v3imp_target"})
	if !strings.Contains(obs, "impact of zz_v3imp_target (source: tree-sitter, depth<=3): 4 dependents") {
		t.Fatalf("missing the honest impact header, got: %s", obs)
	}
	if !strings.Contains(obs, "(depth 1, confidence 1.00)") {
		t.Fatalf("direct callers must render depth 1 confidence 1.00, got: %s", obs)
	}
	if !strings.Contains(obs, "(depth 2, confidence 0.33)") {
		t.Fatalf("the transitive caller must decay to 1/3 at depth 2, got: %s", obs)
	}
	if !strings.Contains(obs, "high") || !strings.Contains(obs, "medium") {
		t.Fatalf("dependents must be grouped by confidence tier (high/medium), got: %s", obs)
	}
	if !strings.Contains(obs, "zz_v3imp_caller") || !strings.Contains(obs, "zz_v3imp_outer") {
		t.Fatalf("both dependent symbols must be listed, got: %s", obs)
	}
	if !strings.Contains(obs, "c1.rs:1-3") {
		t.Fatalf("dependent lines must carry path:start-end, got: %s", obs)
	}
	if strings.Contains(obs, "truncated") {
		t.Fatalf("an un-truncated report must not claim truncation, got: %s", obs)
	}
}

// TestCodeImpactNotesTruncation: more dependents than the kernel's default
// limit (50) renders the limit plus an explicit truncated note.
func TestCodeImpactNotesTruncation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	var src strings.Builder
	src.WriteString("pub fn zz_v3trunc_hub() {}\n")
	for i := 0; i < 55; i++ {
		fmt.Fprintf(&src, "pub fn zz_v3trunc_c%02d() {\n    zz_v3trunc_hub();\n}\n", i)
	}
	os.WriteFile(filepath.Join(ws, "many.rs"), []byte(src.String()), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.impact", Name: "zz_v3trunc_hub"})
	if !strings.Contains(obs, "50 dependents") {
		t.Fatalf("the report must be capped at the kernel's default limit (50), got: %s", obs)
	}
	if !strings.Contains(obs, "truncated") {
		t.Fatalf("a capped report must carry a truncated note, got: %s", obs)
	}
}

// TestCodeImpactDeniedByPolicy: impact is derived data — a CodeIndex-denied
// session gets DENIED, like every code.* tool.
func TestCodeImpactDeniedByPolicy(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3imp_locked() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", &kernel.OrgPolicy{
		BundleTOML: "name = \"locked\"\ndeny_capabilities = [\"CodeIndex\"]\n",
	})
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.impact", Name: "zz_v3imp_locked"})
	if !strings.Contains(obs, "DENIED") {
		t.Fatalf("a CodeIndex-denied session must get DENIED from code.impact, got: %s", obs)
	}
}

// TestCodeImpactRequiresName: argument validation matches the other by-name
// code.* tools.
func TestCodeImpactRequiresName(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")
	obs := d.executeAction(sess, task, &action{Tool: "code.impact"})
	if !strings.Contains(obs, "name") {
		t.Fatalf("code.impact without a name must ask for one, got: %s", obs)
	}
}

// ---- B. observable degrade: code.search channel headers ---------------------

// TestCodeSearchHeaderNoProvider: with zero embeddings providers the header
// states the semantic channel is off and why, and the degrade reason lands in
// the audit chain (reason only, never content).
func TestCodeSearchHeaderNoProvider(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3hdr_np() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3hdr_np"})
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(no-provider)" {
		t.Fatalf("first line = %q, want %q", got, "channels: keyword:on semantic:off(no-provider)")
	}
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("keyword hits must still render under the header, got: %s", obs)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_search_degraded")
	if len(payloads) == 0 {
		t.Fatal("a degraded search must record a code_search_degraded audit event")
	}
	if payloads[0]["reason"] != "no-provider" || payloads[0]["semantic"] != "off" {
		t.Fatalf("degrade event must carry reason keys only, got: %v", payloads[0])
	}
}

// TestCodeSearchHeaderSemanticOn: a healthy provider yields semantic:on and no
// degrade event (the happy path adds no chain noise).
func TestCodeSearchHeaderSemanticOn(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	d.router.RegisterEmbeddingsProvider(&v3FakeEmbedder{name: "fake-embed", dims: 2})
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3hdr_on() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3hdr_on"})
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:on" {
		t.Fatalf("first line = %q, want %q", got, "channels: keyword:on semantic:on")
	}
	if payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_search_degraded"); len(payloads) != 0 {
		t.Fatalf("the happy path must not record degrade events, got: %v", payloads)
	}
}

// TestCodeSearchHeaderProviderError: a query-time embed failure degrades to
// keyword-only with an explicit provider-error reason in the header and chain.
func TestCodeSearchHeaderProviderError(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	fake := &v3FakeEmbedder{name: "fake-embed", dims: 2}
	d.router.RegisterEmbeddingsProvider(fake)
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3hdr_pe() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// First search: healthy sync + healthy query embed.
	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3hdr_pe"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("healthy search failed: %s", obs)
	}
	// Provider goes down at query time.
	fake.fail = true
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3hdr_pe"})
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(provider-error)" {
		t.Fatalf("first line = %q, want %q", got, "channels: keyword:on semantic:off(provider-error)")
	}
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("keyword hits must survive a provider error, got: %s", obs)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_search_degraded")
	found := false
	for _, p := range payloads {
		if p["reason"] == "provider-error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a code_search_degraded event with reason provider-error, got: %v", payloads)
	}
}

// TestCodeSearchHeaderDimsMismatch: a query vector whose dims differ from the
// dims recorded at the last successful embed_store must not silently rank as
// noise — the semantic channel reports off(dims-mismatch).
func TestCodeSearchHeaderDimsMismatch(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	fake := &v3FakeEmbedder{name: "fake-embed", dims: 2}
	d.router.RegisterEmbeddingsProvider(fake)
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3hdr_dm() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// Sync stores 2-dim vectors; the provider then switches to 3 dims.
	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3hdr_dm"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("healthy search failed: %s", obs)
	}
	fake.dims = 3
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3hdr_dm"})
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(dims-mismatch)" {
		t.Fatalf("first line = %q, want %q", got, "channels: keyword:on semantic:off(dims-mismatch)")
	}
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("keyword hits must survive a dims mismatch, got: %s", obs)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_search_degraded")
	found := false
	for _, p := range payloads {
		if p["reason"] == "dims-mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a code_search_degraded event with reason dims-mismatch, got: %v", payloads)
	}
}

// TestCodeSearchHeaderHonestWhenVectorStoreIsEmpty: "semantic:on" must mean
// the cosine channel actually had vectors to rank. A provider that fails the
// batch sync but answers single-input query embeds used to yield semantic:on
// over an EMPTY embeddings store — the header must degrade with the sync
// failure's reason instead.
func TestCodeSearchHeaderHonestWhenVectorStoreIsEmpty(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	d.router.RegisterEmbeddingsProvider(&v3FakeEmbedder{name: "fake-embed", dims: 2, failMulti: true})
	// Two files -> the sync batch has more than one input and fails, while
	// the single-input query embed keeps succeeding.
	os.WriteFile(filepath.Join(ws, "a.rs"), []byte("pub fn zz_v3empty_a() {}\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "b.rs"), []byte("pub fn zz_v3empty_b() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3empty_a"})
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(provider-error)" {
		t.Fatalf("first line = %q, want %q (the vector store is empty — claiming semantic:on is a lie)",
			got, "channels: keyword:on semantic:off(provider-error)")
	}
	if !strings.Contains(obs, "a.rs") {
		t.Fatalf("keyword hits must still render, got: %s", obs)
	}
	pending, err := d.kern.IndexPendingChunks(sess.SessionID, d.embeddingsModelID(), 1)
	if err != nil {
		t.Fatalf("pending_chunks: %v", err)
	}
	if pending.TotalPending == 0 {
		t.Fatal("fixture broken: the backlog should be unembedded")
	}
}

// TestCodeSearchSemanticRecoversAfterSyncFailure: a failed build-time sync
// must not degrade the session permanently — the next code.search retries the
// sync, and once the provider recovers the backlog drains, daemon.status
// reads healthy again, and semantic:on is true again.
func TestCodeSearchSemanticRecoversAfterSyncFailure(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	fake := &v3FakeEmbedder{name: "fake-embed", dims: 2, fail: true}
	d.router.RegisterEmbeddingsProvider(fake)
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3heal_marker() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3heal_marker"})
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(provider-error)" {
		t.Fatalf("first line = %q, want provider-error while the provider is down", got)
	}

	// The provider recovers: the next search must heal the backlog, not
	// keep the session degraded until an unrelated write.
	fake.fail = false
	obs = d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3heal_marker"})
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:on" {
		t.Fatalf("first line = %q, want semantic:on after the provider recovered", got)
	}
	pending, err := d.kern.IndexPendingChunks(sess.SessionID, d.embeddingsModelID(), 1)
	if err != nil {
		t.Fatalf("pending_chunks: %v", err)
	}
	if pending.TotalPending != 0 {
		t.Fatalf("semantic:on with %d chunks still unembedded — the backlog must drain first", pending.TotalPending)
	}
	entry := v3CodeIntelStatus(t, d, sess.SessionID)
	if entry["semantic"] != "on" {
		t.Fatalf("daemon.status must read healthy after recovery, got: %v", entry)
	}
	if s, _ := entry["last_sync_error"].(string); s != "" {
		t.Fatalf("a recovered sync must clear last_sync_error, got: %v", entry)
	}
}

// TestCodeSearchHeaderDimsMismatchWithoutInProcessMemory: stored dims come
// from the kernel, not only from this process's memory of the last
// embed_store — after a restart (no codeIntelStatus entry) a provider whose
// dims changed must degrade as dims-mismatch, not claim semantic:on while
// the cosine channel skips every stored row.
func TestCodeSearchHeaderDimsMismatchWithoutInProcessMemory(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	fake := &v3FakeEmbedder{name: "fake-embed", dims: 2}
	d.router.RegisterEmbeddingsProvider(fake)
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3amnesia_marker() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// Healthy sync at dims 2.
	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3amnesia_marker"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("healthy search failed: %s", obs)
	}
	// Simulate a daemon restart: the in-process dims reference is gone, but
	// the kernel store still holds the 2-dim vectors. The provider now
	// answers 3-dim vectors.
	d.codeIntelStatus.Delete(sess.SessionID)
	fake.dims = 3

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3amnesia_marker"})
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(dims-mismatch)" {
		t.Fatalf("first line = %q, want dims-mismatch from the kernel's channel counters", got)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_search_degraded")
	found := false
	for _, p := range payloads {
		if p["reason"] == "dims-mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a code_search_degraded event with reason dims-mismatch, got: %v", payloads)
	}
}

// ---- B. observable degrade: code.def / code.refs precision headers ---------

// TestCodeLookupHeaderPrecisionLSP: a working language server labels results
// precision:lsp, and the happy path records no degrade event.
func TestCodeLookupHeaderPrecisionLSP(t *testing.T) {
	t.Setenv("CARINA_FAKE_LSP", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: os.Args[0], args: []string{"-test.run=^TestFakeLSPServerHelper$"}, langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("pub fn zz_v3prec_target() {}\n\npub fn caller() {\n    zz_v3prec_target();\n}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_v3prec_target"})
	if !strings.Contains(obs, "definitions (1, precision:lsp):") {
		t.Fatalf("code.def must label the LSP path precision:lsp, got: %s", obs)
	}
	obs = d.executeAction(sess, task, &action{Tool: "code.refs", Name: "zz_v3prec_target"})
	if !strings.Contains(obs, "precision:lsp") {
		t.Fatalf("code.refs must label the LSP path precision:lsp, got: %s", obs)
	}
	if payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_lookup_degraded"); len(payloads) != 0 {
		t.Fatalf("the LSP happy path must not record degrade events, got: %v", payloads)
	}
}

// TestCodeLookupHeaderLSPUnavailable: an absent server binary degrades with
// precision:tree-sitter(lsp-unavailable) and the reason lands in the chain.
func TestCodeLookupHeaderLSPUnavailable(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: "definitely-not-a-real-lsp-binary", langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3prec_absent() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_v3prec_absent"})
	if !strings.Contains(obs, "precision:tree-sitter(lsp-unavailable)") {
		t.Fatalf("absent server must degrade as precision:tree-sitter(lsp-unavailable), got: %s", obs)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_lookup_degraded")
	if len(payloads) == 0 {
		t.Fatal("a degraded lookup must record a code_lookup_degraded audit event")
	}
	p := payloads[0]
	if p["reason"] != "lsp-unavailable" || p["precision"] != "tree-sitter" || p["tool"] != "code.def" {
		t.Fatalf("degrade event must carry tool/precision/reason keys, got: %v", p)
	}
}

// TestCodeLookupHeaderReadDenied: a kernel FileRead denial on the anchor file
// degrades with precision:tree-sitter(read-denied) — and no server ever sees
// the content.
func TestCodeLookupHeaderReadDenied(t *testing.T) {
	t.Setenv("CARINA_FAKE_LSP", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: os.Args[0], args: []string{"-test.run=^TestFakeLSPServerHelper$"}, langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	outside := filepath.Join(t.TempDir(), "outside.rs")
	os.WriteFile(outside, []byte("pub fn zz_v3prec_denied() {}\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3prec_denied() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// Build while readable, then swap the indexed path to a symlink that
	// resolves outside the workspace (the kernel denies the read).
	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3prec_denied"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("index build failed: %s", obs)
	}
	os.Remove(filepath.Join(ws, "main.rs"))
	if err := os.Symlink(outside, filepath.Join(ws, "main.rs")); err != nil {
		t.Fatal(err)
	}

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_v3prec_denied"})
	if !strings.Contains(obs, "precision:tree-sitter(read-denied)") {
		t.Fatalf("a denied anchor read must degrade as precision:tree-sitter(read-denied), got: %s", obs)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_lookup_degraded")
	found := false
	for _, p := range payloads {
		if p["reason"] == "read-denied" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a code_lookup_degraded event with reason read-denied, got: %v", payloads)
	}
}

// TestCodeLookupHeaderNoBoundaryMatch: when the indexed anchor line no longer
// contains the symbol (stale index), the LSP query cannot be aimed — the
// fallback states precision:tree-sitter(no-boundary-match).
func TestCodeLookupHeaderNoBoundaryMatch(t *testing.T) {
	t.Setenv("CARINA_FAKE_LSP", "1")
	// The V4 staleness sweep would heal this fixture's out-of-band rewrite
	// before code.def runs (that is its whole point); switch it off to keep
	// the index stale — the exact scenario this degrade path exists for.
	t.Setenv("CARINA_INDEX_SWEEP", "off")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: os.Args[0], args: []string{"-test.run=^TestFakeLSPServerHelper$"}, langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3prec_moved() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3prec_moved"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("index build failed: %s", obs)
	}
	// The definition moves far below the indexed line without invalidation.
	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("// pad\n// pad\n// pad\n// pad\npub fn zz_v3prec_moved() {}\n"), 0o600)

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_v3prec_moved"})
	if !strings.Contains(obs, "precision:tree-sitter(no-boundary-match)") {
		t.Fatalf("an unfindable anchor position must degrade as precision:tree-sitter(no-boundary-match), got: %s", obs)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_lookup_degraded")
	found := false
	for _, p := range payloads {
		if p["reason"] == "no-boundary-match" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a code_lookup_degraded event with reason no-boundary-match, got: %v", payloads)
	}
}

// ---- B. observable degrade: embedding sync failures -------------------------

// TestEmbeddingSyncFailureIsObservable: a failing provider no longer degrades
// silently — the failure surfaces as a daemon log line, an
// embedding_sync_failed audit event (reason only), and a per-session
// daemon.status code_intel entry. The calling tool still succeeds.
func TestEmbeddingSyncFailureIsObservable(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	d.router.RegisterEmbeddingsProvider(&v3FakeEmbedder{name: "fake-embed", dims: 2, fail: true})
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3sync_fail() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	var obs string
	out := v3CaptureStdout(t, func() {
		obs = d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3sync_fail"})
	})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("a sync failure must never fail the calling tool, got: %s", obs)
	}
	if !strings.Contains(out, "carina-daemon: embedding sync failed (session "+sess.SessionID+")") {
		t.Fatalf("missing the embedding-sync failure log line, got: %q", out)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "embedding_sync_failed")
	if len(payloads) == 0 {
		t.Fatal("a failed sync must record an embedding_sync_failed audit event")
	}
	if payloads[0]["reason"] != "provider-error" {
		t.Fatalf("sync-failure event must classify the reason, got: %v", payloads[0])
	}
	entry := v3CodeIntelStatus(t, d, sess.SessionID)
	if entry["model_id"] != "fake-embed/unit-embed-model" {
		t.Fatalf("code_intel entry must carry the model id, got: %v", entry)
	}
	if s, _ := entry["last_sync_error"].(string); s == "" {
		t.Fatalf("code_intel entry must expose the last sync error, got: %v", entry)
	}
	if entry["reason"] != "provider-error" {
		t.Fatalf("code_intel entry must classify the reason, got: %v", entry)
	}
}

// TestEnvModelTargetingUnknownProviderDisablesSemanticLayer: the no-fallback
// guard must cover every "provider/model" override, not just the known
// backend table — an unknown prefix ("cohere/…") used to fall through to
// Router.Embed's registration-order fallback, egressing workspace chunks to
// a provider the user did not select and storing vectors under a false
// model_id.
func TestEnvModelTargetingUnknownProviderDisablesSemanticLayer(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "cohere/embed-english-v3")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	// Only "openai" is registered; the override names a provider the router
	// has never heard of.
	fake := &daemonFakeEmbedder{name: "openai"}
	d.router.RegisterEmbeddingsProvider(fake)

	if got := d.embeddingsModelID(); got != "" {
		t.Fatalf("targeting an unknown provider must disable the semantic layer, got %q", got)
	}

	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("pub fn zz_unknown_provider_marker() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_unknown_provider_marker"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("keyword search must keep working, got: %s", obs)
	}
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(no-provider)" {
		t.Fatalf("first line = %q, want no-provider (the selected provider is not registered)", got)
	}
	if err := d.syncEmbeddings(sess.SessionID); err != nil {
		t.Fatalf("sync must degrade silently: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("workspace content must never reach a provider the user did not select (calls=%d)", fake.calls)
	}
}

// ---- C. reranker seam --------------------------------------------------------

// fakeReranker is the seam-injected test double: a permutation function, an
// optional hard failure, and call recording.
type fakeReranker struct {
	name  string
	perm  func(n int) []int
	err   error
	calls int
}

func (f *fakeReranker) Name() string { return f.name }
func (f *fakeReranker) Rerank(_ context.Context, _ string, cands []rerankCandidate) ([]int, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.perm(len(cands)), nil
}

// v3RerankFixture builds a daemon with two deterministic search hits and
// returns the kernel-order paths (reranker off).
func v3RerankFixture(t *testing.T) (*Daemon, *sessionstore.Session, *scheduler.Task, []string) {
	t.Helper()
	d, ws := newLoopDaemon(t)
	t.Cleanup(func() { d.Close() })
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "a.rs"),
		[]byte("pub fn zz_v3rr_alpha() {\n    let zz_v3rr_needle = 1;\n}\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "b.rs"),
		[]byte("pub fn zz_v3rr_beta() {\n    let zz_v3rr_needle = 2;\n}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	baseline := v3HitPaths(obs)
	if len(baseline) != 2 {
		t.Fatalf("fixture must yield exactly 2 hits, got %v in: %s", baseline, obs)
	}
	if strings.Contains(v3FirstLine(obs), "rerank") {
		t.Fatalf("with no reranker configured the header must not mention rerank, got: %s", v3FirstLine(obs))
	}
	return d, sess, task, baseline
}

// TestRerankerEnvResolutionDefaultsOff: unset means the stage is skipped;
// an unrecognized CARINA_RERANKER stays off with one observable log line.
func TestRerankerEnvResolutionDefaultsOff(t *testing.T) {
	t.Setenv("CARINA_RERANKER", "")
	if r := rerankerFromEnv(); r != nil {
		t.Fatalf("unset CARINA_RERANKER must resolve no reranker, got %v", r.Name())
	}
	t.Setenv("CARINA_RERANKER", "definitely-unknown-reranker")
	var r Reranker
	out := v3CaptureStdout(t, func() { r = rerankerFromEnv() })
	if r != nil {
		t.Fatalf("an unrecognized reranker must stay off, got %v", r.Name())
	}
	if !strings.Contains(out, "definitely-unknown-reranker") {
		t.Fatalf("an unrecognized reranker must be observable via one log line, got: %q", out)
	}
}

// TestRerankerSeamReordersHits: a seam-injected fake reorders governed results
// (a permutation — never inject/drop/rewrite) and the header names it.
func TestRerankerSeamReordersHits(t *testing.T) {
	d, sess, task, baseline := v3RerankFixture(t)
	fake := &fakeReranker{name: "fake-rr", perm: func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = n - 1 - i
		}
		return out
	}}
	prev := configuredReranker
	configuredReranker = func() Reranker { return fake }
	defer func() { configuredReranker = prev }()

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	got := v3HitPaths(obs)
	want := []string{baseline[1], baseline[0]}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("reranked order = %v, want reversed kernel order %v (obs: %s)", got, want, obs)
	}
	if !strings.Contains(v3FirstLine(obs), "rerank:fake-rr") {
		t.Fatalf("the header must name the active reranker, got: %s", v3FirstLine(obs))
	}
	if fake.calls == 0 {
		t.Fatal("the injected reranker must have been consulted")
	}
}

// TestRerankerErrorFallsBackToKernelOrder: a reranker failure renders the
// un-reranked kernel order, flags rerank:off(rerank-error), and records the
// degrade event.
func TestRerankerErrorFallsBackToKernelOrder(t *testing.T) {
	d, sess, task, baseline := v3RerankFixture(t)
	fake := &fakeReranker{name: "fake-rr", err: errors.New("rerank boom")}
	prev := configuredReranker
	configuredReranker = func() Reranker { return fake }
	defer func() { configuredReranker = prev }()

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	got := v3HitPaths(obs)
	if len(got) != 2 || got[0] != baseline[0] || got[1] != baseline[1] {
		t.Fatalf("rerank failure must fall back to kernel order %v, got %v", baseline, got)
	}
	if !strings.Contains(v3FirstLine(obs), "rerank:off(rerank-error)") {
		t.Fatalf("the header must flag the rerank failure, got: %s", v3FirstLine(obs))
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_rerank_degraded")
	if len(payloads) == 0 {
		t.Fatal("a rerank failure must record a code_rerank_degraded audit event")
	}
	if payloads[0]["reranker"] != "fake-rr" || payloads[0]["reason"] != "rerank-error" {
		t.Fatalf("rerank degrade event must carry reranker/reason, got: %v", payloads[0])
	}
}

// TestRerankerInvalidPermutationFallsBack: a wrong-length or duplicate-index
// permutation is a failure — a reranker may reorder but never drop or
// duplicate governed results.
func TestRerankerInvalidPermutationFallsBack(t *testing.T) {
	d, sess, task, baseline := v3RerankFixture(t)
	fake := &fakeReranker{name: "fake-rr", perm: func(n int) []int { return []int{0, 0} }}
	prev := configuredReranker
	configuredReranker = func() Reranker { return fake }
	defer func() { configuredReranker = prev }()

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	got := v3HitPaths(obs)
	if len(got) != 2 || got[0] != baseline[0] || got[1] != baseline[1] {
		t.Fatalf("an invalid permutation must fall back to kernel order %v, got %v", baseline, got)
	}
	if !strings.Contains(v3FirstLine(obs), "rerank:off(rerank-error)") {
		t.Fatalf("the header must flag the invalid permutation, got: %s", v3FirstLine(obs))
	}
	if len(v3StatusEventPayloads(t, d, sess.SessionID, "code_rerank_degraded")) == 0 {
		t.Fatal("an invalid permutation must record a code_rerank_degraded audit event")
	}
}

// ---- D2. UTF-16 LSP columns --------------------------------------------------

// TestCodeDefConvertsUTF16Columns: the daemon must send UTF-16 code-unit
// columns to the server and convert returned columns back to rune columns for
// rendering. The anchor line mixes an emoji (2 UTF-16 units) and a CJK char
// (1 unit): name at byte col 21, UTF-16 col 17, 1-based rune col 17.
func TestCodeDefConvertsUTF16Columns(t *testing.T) {
	t.Setenv("CARINA_FAKE_LSP", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: os.Args[0], args: []string{"-test.run=^TestFakeLSPServerHelper$"}, langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("/* 😀你 */ pub fn zz_v3utf_target() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_v3utf_target"})
	if !strings.Contains(obs, "main.rs:1:") {
		t.Fatalf("the LSP path must answer (fake echo server), got: %s", obs)
	}
	// The fake server echoes the queried position: only a correct
	// byte->UTF-16 outbound and UTF-16->rune inbound conversion renders 17.
	if !strings.Contains(obs, "main.rs:1:17") {
		t.Fatalf("UTF-16 column conversion broken (want main.rs:1:17), got: %s", obs)
	}
	if strings.Contains(obs, "main.rs:1:22") {
		t.Fatalf("byte columns must not be sent/rendered as UTF-16 columns, got: %s", obs)
	}
}

// ---- D3. URI encoding + symlink-canonicalized containment -------------------

// TestFilterWorkspaceLocationsCanonicalizesSymlinks: containment must compare
// canonical paths (macOS /tmp vs /private/tmp) — a location under the real
// directory belongs to a workspace rooted at a symlink to it, and vice versa.
func TestFilterWorkspaceLocationsCanonicalizesSymlinks(t *testing.T) {
	real := t.TempDir()
	canon, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "wslink")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	kept := filterWorkspaceLocations(link, []lsp.Location{
		{Path: filepath.Join(canon, "a.go"), Line: 1, Char: 1},
	})
	if len(kept) != 1 {
		t.Fatalf("a canonical-path location under a symlinked root must be kept, got %+v", kept)
	}
	kept = filterWorkspaceLocations(canon, []lsp.Location{
		{Path: filepath.Join(link, "b.go"), Line: 1, Char: 1},
	})
	if len(kept) != 1 {
		t.Fatalf("a symlinked-path location under the canonical root must be kept, got %+v", kept)
	}
	kept = filterWorkspaceLocations(link, []lsp.Location{
		{Path: filepath.Join(string(filepath.Separator), "definitely-outside", "x.go"), Line: 1, Char: 1},
	})
	if len(kept) != 0 {
		t.Fatalf("out-of-workspace locations must still be dropped, got %+v", kept)
	}
}

// TestFakeLSPServerHelperV3 is not a real test: re-exec'ed with
// CARINA_FAKE_LSP_V3=1 it behaves like a real language server — it
// percent-decodes incoming URIs, canonicalizes the path (EvalSymlinks), and
// echoes the location back with a percent-encoded canonical URI.
func TestFakeLSPServerHelperV3(t *testing.T) {
	if os.Getenv("CARINA_FAKE_LSP_V3") != "1" {
		t.Skip("helper process, spawned by TestCodeDefDecodesEncodedURIsAndCanonicalizesRoots")
	}
	runFakeLSPServerV3(os.Stdin, os.Stdout)
	os.Exit(0)
}

func runFakeLSPServerV3(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	write := func(id int, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(b), b)
	}
	for {
		length := 0
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if n, ok := strings.CutPrefix(line, "Content-Length: "); ok {
				length, _ = strconv.Atoi(n)
			}
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(buf, &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			write(*req.ID, map[string]any{"capabilities": map[string]any{}})
		case "textDocument/definition", "textDocument/references":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Position struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"position"`
			}
			_ = json.Unmarshal(req.Params, &p)
			path := strings.TrimPrefix(p.TextDocument.URI, "file://")
			if u, err := url.Parse(p.TextDocument.URI); err == nil && u.Path != "" {
				path = u.Path
			}
			if canon, err := filepath.EvalSymlinks(path); err == nil {
				path = canon
			}
			echo := (&url.URL{Scheme: "file", Path: path}).String()
			rng := map[string]any{"start": map[string]int{"line": p.Position.Line, "character": p.Position.Character}}
			write(*req.ID, []map[string]any{{"uri": echo, "range": rng}})
		case "shutdown":
			write(*req.ID, nil)
		case "exit":
			return
		default:
			if req.ID != nil {
				write(*req.ID, nil)
			}
		}
	}
}

// TestCodeDefDecodesEncodedURIsAndCanonicalizesRoots: a real-server-shaped
// fake answers with a percent-encoded, symlink-canonicalized URI for a file
// under "a b/你好.rs" — the daemon must decode it and keep it through the
// canonicalized containment check (on macOS the temp workspace root itself is
// a symlink, /var vs /private/var).
func TestCodeDefDecodesEncodedURIsAndCanonicalizesRoots(t *testing.T) {
	t.Setenv("CARINA_FAKE_LSP_V3", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: os.Args[0], args: []string{"-test.run=^TestFakeLSPServerHelperV3$"}, langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	if err := os.MkdirAll(filepath.Join(ws, "a b"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws, "a b", "你好.rs"), []byte("pub fn zz_v3uri_target() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3uri_target"}); !strings.Contains(obs, "你好.rs") {
		t.Fatalf("scan/index must cover space+CJK paths, got: %s", obs)
	}
	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_v3uri_target"})
	if strings.Contains(obs, "%20") || strings.Contains(obs, "%E4") {
		t.Fatalf("percent-encoded server URIs must be decoded before rendering, got: %s", obs)
	}
	if !strings.Contains(obs, "a b/你好.rs") {
		t.Fatalf("the decoded path must render, got: %s", obs)
	}
	// The LSP location (path:line:char) must survive the canonicalized
	// containment check; the tree-sitter fallback would render 1-1 instead.
	if !strings.Contains(obs, "你好.rs:1:8") {
		t.Fatalf("a canonicalized in-workspace LSP location must be kept (want 你好.rs:1:8), got: %s", obs)
	}
}

// ---- B/D. governed LSP children: scrubbed env + egress chokepoint -----------

// TestFakeLSPEnvProbeHelper is not a real test: re-exec'ed with
// CARINA_LSP_ENV_PROBE=1 it acts as a language server whose definition
// answer names a marker file describing the environment it was spawned
// with — leaked.rs (a credential variable is visible), proxied.rs (no
// credential, egress proxy env present), or clean.rs.
func TestFakeLSPEnvProbeHelper(t *testing.T) {
	if os.Getenv("CARINA_LSP_ENV_PROBE") != "1" {
		t.Skip("helper process, spawned by TestCodeDefSpawnsLSPWithGovernedEnv")
	}
	marker := "clean"
	if os.Getenv("ZZ_PROBE_API_KEY") != "" {
		marker = "leaked"
	} else if os.Getenv("HTTPS_PROXY") != "" {
		marker = "proxied"
	}
	runFakeLSPEnvProbeServer(os.Stdin, os.Stdout, marker)
	os.Exit(0)
}

// runFakeLSPEnvProbeServer answers definition queries with
// <request dir>/<marker>.rs so the test can read the child's environment
// state out of the rendered location.
func runFakeLSPEnvProbeServer(r io.Reader, w io.Writer, marker string) {
	br := bufio.NewReader(r)
	write := func(id int, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(b), b)
	}
	for {
		length := 0
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if n, ok := strings.CutPrefix(line, "Content-Length: "); ok {
				length, _ = strconv.Atoi(n)
			}
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(buf, &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			write(*req.ID, map[string]any{"capabilities": map[string]any{}})
		case "textDocument/definition", "textDocument/references":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(req.Params, &p)
			path := strings.TrimPrefix(p.TextDocument.URI, "file://")
			if u, err := url.Parse(p.TextDocument.URI); err == nil && u.Path != "" {
				path = u.Path
			}
			echo := (&url.URL{Scheme: "file", Path: filepath.Join(filepath.Dir(path), marker+".rs")}).String()
			rng := map[string]any{"start": map[string]int{"line": 0, "character": 0}}
			write(*req.ID, []map[string]any{{"uri": echo, "range": rng}})
		case "shutdown":
			write(*req.ID, nil)
		case "exit":
			return
		default:
			if req.ID != nil {
				write(*req.ID, nil)
			}
		}
	}
}

// TestLSPEnvScrubsCredentialsAndAddsEgressProxy: the environment handed to
// language-server children drops credential-bearing variables and appends the
// governed egress proxy overrides — LSP children must not be an ungoverned
// network path holding daemon secrets.
func TestLSPEnvScrubsCredentialsAndAddsEgressProxy(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-super-secret")
	t.Setenv("ZZ_KEEP_ME", "kept")
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.egressURL = "http://127.0.0.1:19999"

	env := d.lspEnv()
	joined := "\n" + strings.Join(env, "\n") + "\n"
	if strings.Contains(joined, "OPENAI_API_KEY=") {
		t.Fatal("lspEnv must scrub credential variables")
	}
	if !strings.Contains(joined, "\nZZ_KEEP_ME=kept\n") {
		t.Fatal("lspEnv must keep ordinary variables")
	}
	if !strings.Contains(joined, "\nHTTPS_PROXY=http://127.0.0.1:19999\n") {
		t.Fatalf("lspEnv must route children through the egress proxy, got: %q", env)
	}
}

// TestCodeDefSpawnsLSPWithGovernedEnv: an actually spawned language-server
// child must not see daemon credentials and must see the egress proxy
// overrides — the probe server names a marker file for the state it observed.
func TestCodeDefSpawnsLSPWithGovernedEnv(t *testing.T) {
	t.Setenv("CARINA_LSP_ENV_PROBE", "1")
	t.Setenv("ZZ_PROBE_API_KEY", "sk-super-secret")
	t.Setenv("HTTPS_PROXY", "")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	d.egressURL = "http://127.0.0.1:19999"
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: os.Args[0], args: []string{"-test.run=^TestFakeLSPEnvProbeHelper$"}, langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3env_target() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_v3env_target"})
	if strings.Contains(obs, "leaked.rs") {
		t.Fatalf("the LSP child saw a daemon credential variable, got: %s", obs)
	}
	if !strings.Contains(obs, "proxied.rs") {
		t.Fatalf("the LSP child must run under the egress proxy env (want proxied.rs), got: %s", obs)
	}
}

// ---- D4. dims-mismatch / zero-vector sync degrade ---------------------------

// TestSyncEmbeddingsWrongDimsDegradesCleanly: a provider answering a batch
// with inconsistent dims must not poison the store — the sync errors, nothing
// is stored, code.search stays keyword-only, and the failure is observable in
// the log, the audit chain, and daemon.status.
func TestSyncEmbeddingsWrongDimsDegradesCleanly(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	d.router.RegisterEmbeddingsProvider(&v3FakeEmbedder{name: "fake-embed", dims: 2, ragged: true})
	os.WriteFile(filepath.Join(ws, "a.rs"), []byte("pub fn zz_v3dims_a() {}\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "b.rs"), []byte("pub fn zz_v3dims_b() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	var obs string
	out := v3CaptureStdout(t, func() {
		obs = d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3dims_a"})
	})
	if !strings.Contains(obs, "a.rs") {
		t.Fatalf("keyword search must survive a wrong-dims provider, got: %s", obs)
	}
	if !strings.Contains(out, "embedding sync failed (session "+sess.SessionID+")") {
		t.Fatalf("missing the sync failure log line, got: %q", out)
	}

	if err := d.syncEmbeddings(sess.SessionID); err == nil || !strings.Contains(err.Error(), "dims") {
		t.Fatalf("a wrong-dims batch must error the sync, got: %v", err)
	}
	pending, err := d.kern.IndexPendingChunks(sess.SessionID, d.embeddingsModelID(), 1)
	if err != nil {
		t.Fatalf("pending_chunks: %v", err)
	}
	if pending.TotalPending < 2 {
		t.Fatalf("nothing may be stored from a wrong-dims batch, %d pending", pending.TotalPending)
	}
	raw, err := d.searchIndex(sess.SessionID, "zz_v3dims_a", 10)
	if err != nil {
		t.Fatalf("searchIndex: %v", err)
	}
	if strings.Contains(string(raw), `"vector"`) {
		t.Fatalf("no hit may claim a vector source after a failed sync: %s", raw)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "embedding_sync_failed")
	found := false
	for _, p := range payloads {
		if p["reason"] == "dims-mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an embedding_sync_failed event with reason dims-mismatch, got: %v", payloads)
	}
	entry := v3CodeIntelStatus(t, d, sess.SessionID)
	if entry["reason"] != "dims-mismatch" {
		t.Fatalf("code_intel entry must classify the reason as dims-mismatch, got: %v", entry)
	}
}

// TestSyncEmbeddingsZeroVectorsDegradeCleanly: zero-length vectors are a
// provider error — the sync errors, nothing is stored, keyword search keeps
// working, and the failure is on the chain.
func TestSyncEmbeddingsZeroVectorsDegradeCleanly(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	d.router.RegisterEmbeddingsProvider(&v3FakeEmbedder{name: "fake-embed", dims: 2, zero: true})
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v3zero_marker() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3zero_marker"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("keyword search must survive zero-length vectors, got: %s", obs)
	}
	if err := d.syncEmbeddings(sess.SessionID); err == nil {
		t.Fatal("zero-length vectors must error the sync")
	}
	pending, err := d.kern.IndexPendingChunks(sess.SessionID, d.embeddingsModelID(), 1)
	if err != nil {
		t.Fatalf("pending_chunks: %v", err)
	}
	if pending.TotalPending == 0 {
		t.Fatal("nothing may be stored from a zero-vector batch")
	}
	if len(v3StatusEventPayloads(t, d, sess.SessionID, "embedding_sync_failed")) == 0 {
		t.Fatal("a zero-vector sync failure must record an embedding_sync_failed audit event")
	}
}
