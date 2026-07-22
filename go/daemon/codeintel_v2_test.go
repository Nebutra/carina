package daemon

// V2 semantic-layer tests (docs/plans/code-intelligence.md): embeddings
// provider registration + adapter, the best-effort embedding sync pipeline
// with its mandated degrade paths, and the LSP-first code.def / code.refs
// tools with tree-sitter fallback and workspace scoping.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/lsp"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

// daemonFakeEmbedder is an in-process embeddings provider: deterministic
// vectors, call/batch recording, optional hard failure.
type daemonFakeEmbedder struct {
	name       string
	fail       bool
	calls      int
	batchSizes []int
	maxInput   int
}

func (f *daemonFakeEmbedder) Name() string { return f.name }
func (f *daemonFakeEmbedder) Embed(_ context.Context, req modelrouter.EmbeddingsRequest) (*modelrouter.EmbeddingsResponse, error) {
	f.calls++
	f.batchSizes = append(f.batchSizes, len(req.Inputs))
	for _, input := range req.Inputs {
		if len(input) > f.maxInput {
			f.maxInput = len(input)
		}
	}
	if f.fail {
		return nil, errors.New("fake embedder down")
	}
	vectors := make([][]float32, len(req.Inputs))
	for i := range req.Inputs {
		vectors[i] = []float32{1, 0}
	}
	model := req.Model
	if model == "" || model == "default" {
		model = "unit-embed-model"
	}
	return &modelrouter.EmbeddingsResponse{
		Provider: f.name, Model: model, Vectors: vectors, InputTokens: len(req.Inputs),
	}, nil
}

// TestEmbeddingsRegistrationRequiresCredential: embeddings providers register
// only when a credential resolves (degrade before any network), and never in
// offline mode — the deliberate delta from chat providers.
func TestEmbeddingsRegistrationRequiresCredential(t *testing.T) {
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}

	// No credential anywhere: nothing registers.
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VOYAGE_API_KEY", "")
	bare := modelrouter.New()
	registerEmbeddingsProviders(bare, false, nil, store)
	if bare.HasEmbeddingsProvider() {
		t.Fatal("without credentials no embeddings provider may register")
	}

	// A resolvable BYOK key registers its provider.
	t.Setenv("OPENAI_API_KEY", "sk-embed-test")
	keyed := modelrouter.New()
	registerEmbeddingsProviders(keyed, false, nil, store)
	if !keyed.HasEmbeddingsProvider() {
		t.Fatal("a resolvable OPENAI_API_KEY must register the openai embeddings provider")
	}
	disabled := modelrouter.New()
	registerEmbeddingsProviders(disabled, false, []string{"OPENAI"}, store)
	if disabled.HasEmbeddingsProviderNamed("openai") {
		t.Fatal("disabled openai embeddings provider must not register")
	}

	// Offline mode registers nothing even with a key.
	offline := modelrouter.New()
	registerEmbeddingsProviders(offline, true, nil, store)
	if offline.HasEmbeddingsProvider() {
		t.Fatal("offline mode must not register embeddings providers")
	}
}

// TestOpenAIEmbeddingsAdapter: the adapter posts the OpenAI-compatible
// /embeddings request (model, input array, encoding_format=float) with a
// Bearer credential and parses vectors + input-token usage.
func TestOpenAIEmbeddingsAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-embed" {
			t.Fatalf("authorization header = %q", got)
		}
		var body struct {
			Model          string   `json:"model"`
			Input          []string `json:"input"`
			EncodingFormat string   `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "text-embedding-3-small" {
			t.Fatalf("model = %q", body.Model)
		}
		if len(body.Input) != 2 || body.Input[0] != "alpha" || body.Input[1] != "beta" {
			t.Fatalf("input = %+v", body.Input)
		}
		if body.EncodingFormat != "float" {
			t.Fatalf("encoding_format = %q", body.EncodingFormat)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}],` +
			`"model":"text-embedding-3-small","usage":{"prompt_tokens":7,"total_tokens":7}}`))
	}))
	defer srv.Close()

	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAPIKey("openai", "sk-embed", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIEmbeddingsProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL + "/v1", defaultModel: "text-embedding-3-small",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}, encodingFormat: "float"}

	resp, err := p.Embed(context.Background(), modelrouter.EmbeddingsRequest{
		Model: "default", Inputs: []string{"alpha", "beta"},
	})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if resp.Provider != "openai" || resp.Model != "text-embedding-3-small" {
		t.Fatalf("bad response identity: %+v", resp)
	}
	if len(resp.Vectors) != 2 || len(resp.Vectors[0]) != 2 || resp.Vectors[1][1] != 0.4 {
		t.Fatalf("bad vectors: %+v", resp.Vectors)
	}
	if resp.InputTokens != 7 {
		t.Fatalf("input tokens = %d, want 7", resp.InputTokens)
	}
}

// TestSyncEmbeddingsSkipsSilentlyWithoutProvider: the mandated degrade path —
// zero embeddings providers means syncEmbeddings is a silent no-op and
// code.search behaves exactly V1.
func TestSyncEmbeddingsSkipsSilentlyWithoutProvider(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("pub fn zz_degrade_marker() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if d.router.HasEmbeddingsProvider() {
		t.Fatal("offline daemon must have no embeddings provider")
	}
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_degrade_marker"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("V1 keyword search must keep working, got: %s", obs)
	}
	if err := d.syncEmbeddings(sess.SessionID); err != nil {
		t.Fatalf("no-provider sync must skip silently (nil error), got: %v", err)
	}
}

// TestSyncEmbeddingsPipelineWithFakeProvider: pending chunks flow through the
// provider in bounded batches (<=64 inputs, <=maxEmbedChars each) until the
// kernel backlog drains, under the "<provider>/<model>" index key.
func TestSyncEmbeddingsPipelineWithFakeProvider(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	fake := &daemonFakeEmbedder{name: "fake-embed"}
	d.router.RegisterEmbeddingsProvider(fake)

	// Enough symbols for several chunks, plus one chunk far over the embed cap.
	var src strings.Builder
	for i := 0; i < 70; i++ {
		fmt.Fprintf(&src, "pub fn zz_pipeline_fn_%03d() {}\n\n", i)
	}
	src.WriteString("pub fn zz_pipeline_huge() {\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&src, "    let filler_%03d = \"%s\";\n", i, strings.Repeat("x", 160))
	}
	src.WriteString("}\n")
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte(src.String()), 0o600)

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// First code.* use builds the index; the sync must then drain the backlog.
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_pipeline_fn_001"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("code.search must hit main.rs, got: %s", obs)
	}
	if err := d.syncEmbeddings(sess.SessionID); err != nil {
		t.Fatalf("sync with a working provider must succeed: %v", err)
	}

	modelID := d.embeddingsModelID()
	if modelID != "fake-embed/unit-embed-model" {
		t.Fatalf("index model id must be provider/model, got %q", modelID)
	}
	if fake.calls == 0 {
		t.Fatal("the provider must have been asked to embed pending chunks")
	}
	for _, size := range fake.batchSizes {
		if size == 0 || size > 64 {
			t.Fatalf("batch sizes must be 1..64, got %v", fake.batchSizes)
		}
	}
	if fake.maxInput > maxEmbedChars {
		t.Fatalf("chunk text must be truncated to %d chars, saw %d", maxEmbedChars, fake.maxInput)
	}
	pending, err := d.kern.IndexPendingChunks(sess.SessionID, modelID, 1)
	if err != nil {
		t.Fatalf("pending_chunks: %v", err)
	}
	if pending.TotalPending != 0 {
		t.Fatalf("sync must drain the backlog, %d still pending", pending.TotalPending)
	}
}

// TestCodeSearchFallsBackWhenQueryEmbedFails: with a provider configured,
// code.search first embeds the query — and on provider failure falls back
// cleanly to the plain keyword search (the second mandated degrade path).
func TestCodeSearchFallsBackWhenQueryEmbedFails(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	fake := &daemonFakeEmbedder{name: "fake-embed", fail: true}
	d.router.RegisterEmbeddingsProvider(fake)

	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("pub fn zz_fallback_marker() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_fallback_marker"})
	if !strings.Contains(obs, "main.rs") || !strings.Contains(obs, "zz_fallback_marker") {
		t.Fatalf("embed failure must fall back to plain search, got: %s", obs)
	}
	if fake.calls == 0 {
		t.Fatal("code.search must have attempted to embed the query first")
	}
}

// TestCodeDefAndRefsAreReadOnlyTools: both are pure queries (batchable).
func TestCodeDefAndRefsAreReadOnlyTools(t *testing.T) {
	for _, tool := range []string{"code.def", "code.refs"} {
		if !isReadOnlyTool(tool) {
			t.Fatalf("%s must be read-only (batchable)", tool)
		}
	}
}

// TestCodeDefAndRefsFallBackToTreeSitterWhenLSPAbsent: an absent language
// server binary degrades cleanly to the kernel's tree-sitter results,
// labeled with their honest confidence.
func TestCodeDefAndRefsFallBackToTreeSitterWhenLSPAbsent(t *testing.T) {
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

	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("pub fn zz_def_target() {}\n\npub fn caller() {\n    zz_def_target();\n}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_def_target"})
	if !strings.Contains(obs, "main.rs") || !strings.Contains(obs, "zz_def_target") {
		t.Fatalf("code.def must render the tree-sitter definition, got: %s", obs)
	}
	if !strings.Contains(obs, "tree-sitter") {
		t.Fatalf("fallback results must carry confidence tree-sitter, got: %s", obs)
	}
	if strings.Contains(obs, "confidence=lsp") || strings.Contains(obs, "precision:lsp") {
		t.Fatalf("an absent server must never claim lsp precision, got: %s", obs)
	}

	obs = d.executeAction(sess, task, &action{Tool: "code.refs", Name: "zz_def_target"})
	if !strings.Contains(obs, "main.rs") || !strings.Contains(obs, "tree-sitter") {
		t.Fatalf("code.refs must fall back to tree-sitter references, got: %s", obs)
	}

	// Argument validation matches the other code.* tools.
	if obs := d.executeAction(sess, task, &action{Tool: "code.def"}); !strings.Contains(obs, "name") {
		t.Fatalf("expected a name error, got: %s", obs)
	}
	if obs := d.executeAction(sess, task, &action{Tool: "code.refs"}); !strings.Contains(obs, "name") {
		t.Fatalf("expected a name error, got: %s", obs)
	}
}

// TestCodeDefDeniedByPolicyBeforeLSP: the kernel symbols lookup is the policy
// gate — a CodeIndex denial must surface as DENIED and no LSP may run.
func TestCodeDefDeniedByPolicyBeforeLSP(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_locked_def() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", &kernel.OrgPolicy{
		BundleTOML: "name = \"locked\"\ndeny_capabilities = [\"CodeIndex\"]\n",
	})
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_locked_def"})
	if !strings.Contains(obs, "DENIED") {
		t.Fatalf("a CodeIndex-denied session must get DENIED from code.def, got: %s", obs)
	}
}

// TestFilterWorkspaceLocations: LSP servers never leak paths outside the
// session workspace into rendered results.
func TestFilterWorkspaceLocations(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "ws")
	locs := []lsp.Location{
		{Path: filepath.Join(root, "a.go"), Line: 1, Char: 1},
		{Path: filepath.Join(string(filepath.Separator), "etc", "passwd"), Line: 1, Char: 1},
		{Path: filepath.Join(root, "..", "outside", "b.go"), Line: 2, Char: 2},
		{Path: filepath.Join(root, "sub", "c.go"), Line: 3, Char: 3},
	}
	kept := filterWorkspaceLocations(root, locs)
	if len(kept) != 2 {
		t.Fatalf("expected exactly the 2 in-workspace locations, got %+v", kept)
	}
	for _, loc := range kept {
		if !strings.HasPrefix(loc.Path, root+string(filepath.Separator)) {
			t.Fatalf("out-of-workspace location leaked: %+v", loc)
		}
	}
}

// TestVoyageEmbeddingsAdapterOmitsEncodingFormat: the Voyage /embeddings API
// accepts only null or "base64" for encoding_format — the request must omit
// the field entirely (unlike OpenAI's "float"), and usage arrives as
// total_tokens, not prompt_tokens.
func TestVoyageEmbeddingsAdapterOmitsEncodingFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if v, ok := body["encoding_format"]; ok {
			t.Errorf("voyage requests must not carry encoding_format (API accepts only null or \"base64\"), got %v", v)
		}
		if body["model"] != "voyage-code-3" {
			t.Errorf("model = %v", body["model"])
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.5,0.25]}],` +
			`"model":"voyage-code-3","usage":{"total_tokens":9}}`))
	}))
	defer srv.Close()

	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAPIKey("voyage", "pa-embed", nil); err != nil {
		t.Fatal(err)
	}
	var backend embeddingsBackend
	for _, b := range embeddingsBackends {
		if b.id == "voyage" {
			backend = b
		}
	}
	if backend.id != "voyage" {
		t.Fatal("voyage must stay in the embeddings backend table")
	}
	// Constructed exactly as registerEmbeddingsProviders wires it, with the
	// table's encoding format — only the endpoint is redirected to the stub.
	p := &openAIEmbeddingsProvider{providerBase: providerBase{
		id: backend.id, baseURL: srv.URL + "/v1", defaultModel: backend.defaultModel,
		auth: auth.ProviderChain("voyage", nil, store, nil), client: srv.Client(),
	}, encodingFormat: backend.encodingFormat}

	resp, err := p.Embed(context.Background(), modelrouter.EmbeddingsRequest{
		Model: "default", Inputs: []string{"pub fn a() {}"},
	})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(resp.Vectors) != 1 || len(resp.Vectors[0]) != 2 {
		t.Fatalf("bad vectors: %+v", resp.Vectors)
	}
	if resp.InputTokens != 9 {
		t.Fatalf("voyage usage.total_tokens must be accounted, got %d", resp.InputTokens)
	}
}

// TestEnvModelTargetingUnregisteredBackendDisablesSemanticLayer: a
// CARINA_EMBEDDINGS_MODEL naming a known backend whose credential is absent
// must switch the semantic layer off — never let router fallback send
// workspace chunks to a provider the user did not select.
func TestEnvModelTargetingUnregisteredBackendDisablesSemanticLayer(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "voyage/voyage-code-3")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	// Only openai is registered; the env override targets voyage.
	fake := &daemonFakeEmbedder{name: "openai"}
	d.router.RegisterEmbeddingsProvider(fake)

	if got := d.embeddingsModelID(); got != "" {
		t.Fatalf("targeting an unregistered backend must disable the semantic layer, got %q", got)
	}

	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("pub fn zz_wrong_provider_marker() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_wrong_provider_marker"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("keyword search must keep working, got: %s", obs)
	}
	if err := d.syncEmbeddings(sess.SessionID); err != nil {
		t.Fatalf("sync must degrade silently: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("workspace content must never reach a provider the user did not select (calls=%d)", fake.calls)
	}
}

// TestFindNamePositionRespectsTokenBoundaries: the LSP query position must sit
// on the symbol's own token, not on an earlier longer identifier that merely
// contains the name (e.g. receiver type TaskList vs method Task).
func TestFindNamePositionRespectsTokenBoundaries(t *testing.T) {
	line, char, ok := findNamePosition("func (l *TaskList) Task() *Task {\n\treturn nil\n}\n", 1, "Task")
	if !ok || line != 1 || char != 20 {
		t.Fatalf("want the method token at 1:20, got %d:%d ok=%v", line, char, ok)
	}
	if _, _, ok := findNamePosition("type TaskList struct{}\n", 1, "Task"); ok {
		t.Fatal("a substring inside a longer identifier must not match")
	}
	// Plain occurrences keep working.
	line, char, ok = findNamePosition("pub fn zz_plain() {}\n", 1, "zz_plain")
	if !ok || line != 1 || char != 8 {
		t.Fatalf("want 1:8, got %d:%d ok=%v", line, char, ok)
	}
}

// TestCodeDefLSPReadIsKernelGated: the LSP file read is a file read — it must
// go through the kernel FileRead gate. When the indexed path now resolves
// outside the workspace (symlink swap), no content may be read, no language
// server may spawn, and the tool degrades to tree-sitter.
func TestCodeDefLSPReadIsKernelGated(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	outside := filepath.Join(t.TempDir(), "outside.rs")
	os.WriteFile(outside, []byte("pub fn zz_gated_def() { /* outside-secret */ }\n"), 0o600)
	marker := filepath.Join(t.TempDir(), "lsp-spawned")
	script := filepath.Join(t.TempDir(), "fake-lsp.sh")
	os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nsleep 5\n"), 0o700)
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: script, langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_gated_def() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// Build the index while the file is readable (no LSP involved).
	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_gated_def"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("index build failed: %s", obs)
	}
	// The indexed path now resolves outside the workspace.
	os.Remove(filepath.Join(ws, "main.rs"))
	if err := os.Symlink(outside, filepath.Join(ws, "main.rs")); err != nil {
		t.Fatal(err)
	}

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_gated_def"})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a FileRead-denied path must never reach a language server process")
	}
	if strings.Contains(obs, "outside-secret") {
		t.Fatalf("out-of-workspace content leaked: %s", obs)
	}
	if !strings.Contains(obs, "tree-sitter") {
		t.Fatalf("denied LSP read must degrade to tree-sitter, got: %s", obs)
	}
}

// TestFakeLSPServerHelper is not a real test: re-exec'ed with CARINA_FAKE_LSP=1
// it speaks just enough LSP over stdio to answer initialize, echo the queried
// position back as an in-workspace location, and add one out-of-workspace
// location that the daemon must filter.
func TestFakeLSPServerHelper(t *testing.T) {
	if os.Getenv("CARINA_FAKE_LSP") != "1" {
		t.Skip("helper process, spawned by TestCodeDefAndRefsUseLSPWhenServerAvailable")
	}
	runFakeLSPServer(os.Stdin, os.Stdout)
	os.Exit(0)
}

func runFakeLSPServer(r io.Reader, w io.Writer) {
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
			rng := map[string]any{"start": map[string]int{"line": p.Position.Line, "character": p.Position.Character}}
			write(*req.ID, []map[string]any{
				{"uri": p.TextDocument.URI, "range": rng},
				{"uri": "file:///definitely-outside/other.rs", "range": rng},
			})
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

// TestCodeDefAndRefsUseLSPWhenServerAvailable drives the full LSP happy path
// against a fake server (the re-exec'ed test binary): confidence=lsp results,
// the 1-based/0-based position round-trip, and — critically — the workspace
// scoping filter on returned locations.
func TestCodeDefAndRefsUseLSPWhenServerAvailable(t *testing.T) {
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
		[]byte("pub fn zz_lsp_target() {}\n\npub fn caller() {\n    zz_lsp_target();\n}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.def", Name: "zz_lsp_target"})
	if !strings.Contains(obs, "precision:lsp") {
		t.Fatalf("a working server must yield precision:lsp, got: %s", obs)
	}
	// The fake server echoes the queried position: "pub fn zz_lsp_target" puts
	// the name at 1-based line 1 char 8, so a correct 1-based <-> 0-based
	// round-trip renders exactly main.rs:1:8.
	if !strings.Contains(obs, "main.rs:1:8") {
		t.Fatalf("position round-trip broken, got: %s", obs)
	}
	if strings.Contains(obs, "definitely-outside") {
		t.Fatalf("out-of-workspace LSP locations must be filtered, got: %s", obs)
	}
	if strings.Contains(obs, "definitions (2") {
		t.Fatalf("the filtered location must not be counted, got: %s", obs)
	}

	obs = d.executeAction(sess, task, &action{Tool: "code.refs", Name: "zz_lsp_target"})
	if !strings.Contains(obs, "precision:lsp") || !strings.Contains(obs, "main.rs:1:8") {
		t.Fatalf("code.refs must use the server too, got: %s", obs)
	}
	if strings.Contains(obs, "definitely-outside") {
		t.Fatalf("out-of-workspace reference leaked, got: %s", obs)
	}
}

// TestRunCommandDeletionPrunesIndex: a file deleted by a mutating run command
// (which only clears the built flag — agent.go's risk>0 hook) must vanish
// from search results after the next code.* re-sync, exactly as ensureIndex's
// contract promises.
func TestRunCommandDeletionPrunesIndex(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "a.rs"), []byte("pub fn zz_deleted_by_run() {}\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "b.rs"), []byte("pub fn zz_survives_run() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_deleted_by_run"}); !strings.Contains(obs, "a.rs") {
		t.Fatalf("initial index must hit a.rs, got: %s", obs)
	}

	// What a mutating run command does: delete the file, clear the built flag.
	os.Remove(filepath.Join(ws, "a.rs"))
	d.indexBuilt.Delete(sess.SessionID)

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_deleted_by_run"})
	if !strings.Contains(obs, "no matches") {
		t.Fatalf("deleted file content must be pruned on re-sync, got: %s", obs)
	}
	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_survives_run"}); !strings.Contains(obs, "b.rs") {
		t.Fatalf("surviving files must stay indexed, got: %s", obs)
	}
}
