package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCodeIntelTools drives the three governed code-intelligence tools
// (code.search / code.symbols / code.map) through the same kernel-gated
// dispatch as the agent loop: lazy first-use build, ranked search, symbol
// lookup, repo map, and index invalidation after an applied patch.
func TestCodeIntelTools(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "main.rs"),
		[]byte("pub fn zz_daemon_marker() {}\n\npub fn caller() {\n    zz_daemon_marker();\n}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// The three tools are read-only for policy purposes. Semantic tools still
	// run one per turn because the kernel RPC transport is serialized.
	for _, tool := range []string{"code.search", "code.symbols", "code.map"} {
		if !isReadOnlyTool(tool) {
			t.Fatalf("%s must be read-only", tool)
		}
		if isParallelBatchTool(tool) {
			t.Fatalf("%s must not enter a parallel batch", tool)
		}
	}

	// code.search: lazy build on first use, then a ranked hit.
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_daemon_marker"})
	if !strings.Contains(obs, "main.rs") || !strings.Contains(obs, "zz_daemon_marker") {
		t.Fatalf("code.search should hit main.rs, got: %s", obs)
	}

	// code.symbols: definition plus approximate references.
	obs = d.executeAction(sess, task, &action{Tool: "code.symbols", Name: "zz_daemon_marker"})
	if !strings.Contains(obs, "zz_daemon_marker") || !strings.Contains(obs, "tree-sitter") {
		t.Fatalf("code.symbols should report the definition with confidence, got: %s", obs)
	}

	// code.map: the ranked repo map mentions the file.
	obs = d.executeAction(sess, task, &action{Tool: "code.map"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("code.map should mention main.rs, got: %s", obs)
	}

	// Invalidation: after an applied patch the index reflects the edit.
	repoRoot := repoRootFromHere(t)
	if _, err := os.Stat(filepath.Join(repoRoot, "zig/zig-out/bin/carina-patch-native")); err != nil {
		t.Skip("carina-patch-native not built")
	}
	d.executeAction(sess, task, &action{Tool: "read", Path: "main.rs"})
	obs = d.executeAction(sess, task, &action{Tool: "patch", Path: "main.rs",
		Content: "pub fn zz_daemon_renamed() {}\n"})
	if !strings.Contains(obs, "applied") {
		t.Fatalf("patch should apply, got: %s", obs)
	}
	obs = d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_daemon_renamed"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("code.search should see the patched content, got: %s", obs)
	}
	obs = d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_daemon_marker"})
	if strings.Contains(obs, "main.rs") {
		t.Fatalf("stale pre-patch content must be gone, got: %s", obs)
	}
}

// TestRunToolInvalidatesIndex: writes performed by the agent's `run` tool are
// invisible to the patch hooks, so a mutating command must drop the lazily
// built index flag — the next code.* call then rebuilds against current disk
// (content-hash keyed, so unchanged files are no-ops). Read-only commands
// must not churn the index.
func TestRunToolInvalidatesIndex(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// Read-only command (risk 0): the built index stays valid.
	d.indexBuilt.Store(sess.SessionID, true)
	d.executeAction(sess, task, &action{Tool: "run", Command: []string{"echo", "hi"}})
	if _, ok := d.indexBuilt.Load(sess.SessionID); !ok {
		t.Fatal("read-only command must not invalidate the index")
	}

	// Mutating-capable command (risk > 0): the stale flag must be dropped so
	// code.search cannot serve pre-command snippets as current.
	d.executeAction(sess, task, &action{Tool: "run", Command: []string{"make"}})
	if _, ok := d.indexBuilt.Load(sess.SessionID); ok {
		t.Fatal("mutating command must invalidate the index")
	}
}

// TestCodeSearchNeedsQuery: malformed actions come back as errors, not calls.
func TestCodeIntelArgumentErrors(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search"}); !strings.Contains(obs, "query") {
		t.Fatalf("expected a query error, got: %s", obs)
	}
	if obs := d.executeAction(sess, task, &action{Tool: "code.symbols"}); !strings.Contains(obs, "name") {
		t.Fatalf("expected a name error, got: %s", obs)
	}
}

func TestLightweightRepoMapIsBoundedAndDeterministic(t *testing.T) {
	snap := &sweepSnapshot{stamps: make(map[string]fileStamp)}
	for i := 0; i < 300; i++ {
		snap.stamps[fmt.Sprintf("packages/core/file-%03d.ts", i)] = fileStamp{}
	}
	for i := 0; i < 40; i++ {
		snap.stamps[fmt.Sprintf("apps/desktop/file-%03d.tsx", i)] = fileStamp{}
	}
	snap.stamps["main.go"] = fileStamp{}

	first := lightweightRepoMap(snap)
	second := lightweightRepoMap(snap)
	if first != second {
		t.Fatalf("lightweight map must be deterministic:\nfirst=%s\nsecond=%s", first, second)
	}
	for _, want := range []string{
		"Lightweight workspace map (341 source files",
		"metadata fallback while the semantic index completes",
		"packages/ (300 files)",
		"apps/ (40 files)",
		"(root) (1 file)",
		"Use focused list/search/read actions for details.",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("lightweight map missing %q: %s", want, first)
		}
	}
	if strings.Count(first, "\n") > lightweightRepoMapGroupLimit+2 {
		t.Fatalf("lightweight map exceeded its line budget: %s", first)
	}
}

func TestRenderSemanticRepoMapDeclaresGraphAndProjectionCoverage(t *testing.T) {
	raw := json.RawMessage(`{
		"map":"go/main.go:\n  fn Main (3-7) [#1 in:2 out:1]\n",
		"token_estimate":31,
		"symbols_total":120,
		"symbols_included":18,
		"files_total":44,
		"files_included":12,
		"indexed_files":57,
		"edges_total":310,
		"chunks_total":150,
		"projection":"pagerank-domain-diverse"
	}`)
	got := renderSemanticRepoMap(raw, true)
	for _, want := range []string{
		"Index coverage: complete",
		"57 indexed files, 120 symbols, 310 edges, 150 chunks",
		"12/44 files and 18/120 symbols",
		"pagerank-domain-diverse",
		"[#1 in:2 out:1]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered map missing %q: %s", want, got)
		}
	}
}

func TestLargeWorkspaceCodeMapDefersSemanticBuild(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	d.indexSyncFileLimit = 2
	d.indexBuildBatchSize = 1
	for i := 0; i < 3; i++ {
		path := filepath.Join(ws, "packages", "core", fmt.Sprintf("file-%03d.go", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("package core\n\nfunc Symbol%d() {}\n", i)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "map")

	obs := d.agentCodeMap(sess, task, &action{Tool: "code.map"})
	if !strings.Contains(obs, "Index coverage: semantic index building") || !strings.Contains(obs, "Lightweight workspace map") {
		t.Fatalf("large workspace must return immediate progressive coverage, got: %s", obs)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, built := d.indexBuilt.Load(sess.SessionID); built {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background semantic index did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	obs = d.agentCodeMap(sess, task, &action{Tool: "code.map"})
	for _, want := range []string{"Index coverage: complete", "graph:", "projection:", "Symbol0"} {
		if !strings.Contains(obs, want) {
			t.Fatalf("completed map missing %q: %s", want, obs)
		}
	}
	state, ok := d.loadIndexState(ws)
	if !ok || !state.Complete || state.NextPath != 3 {
		t.Fatalf("background build must commit a resumable ready snapshot, got: %+v, ok=%v", state, ok)
	}
}

func TestCodeMapRestoresCompletedWorkspaceIndex(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n\nfunc RestoredSymbol() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "map")

	first := d.agentCodeMap(sess, task, &action{Tool: "code.map"})
	if !strings.Contains(first, "RestoredSymbol") {
		t.Fatalf("first map did not build the index: %s", first)
	}
	statePath := d.indexStatePath(ws)
	before, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a daemon-local cold start while retaining the workspace DB and
	// completed snapshot. The second request should reopen, not rebuild.
	d.indexBuilt.Delete(sess.SessionID)
	d.indexSnapshot.Delete(sess.SessionID)
	second := d.agentCodeMap(sess, task, &action{Tool: "code.map"})
	if !strings.Contains(second, "Index coverage: complete") || !strings.Contains(second, "RestoredSymbol") {
		t.Fatalf("completed index was not restored: %s", second)
	}
	after, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("restoring an unchanged completed index must not rewrite its state marker")
	}

	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n\nfunc FreshAfterRestart() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.indexBuilt.Delete(sess.SessionID)
	d.indexSnapshot.Delete(sess.SessionID)
	third := d.agentCodeMap(sess, task, &action{Tool: "code.map"})
	if !strings.Contains(third, "FreshAfterRestart") || strings.Contains(third, "RestoredSymbol") {
		t.Fatalf("a stale completion fingerprint must reconcile before claiming complete: %s", third)
	}
}

func TestIndexStateIsAtomicPrivateAndFailClosed(t *testing.T) {
	d := &Daemon{stateDir: t.TempDir()}
	root := filepath.Join(t.TempDir(), "workspace")
	snap := &sweepSnapshot{
		stamps: map[string]fileStamp{
			"main.go": {mtime: 12, size: 34, mode: 0o600},
		},
		scannedAt: 56,
	}
	state := persistedStateFromSnapshot(root, snap, true, 1)
	if err := d.saveIndexState(state); err != nil {
		t.Fatal(err)
	}
	path := d.indexStatePath(root)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("index state permissions = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), root) || strings.Contains(string(data), "main.go") {
		t.Fatalf("orchestration state must not persist workspace paths: %s", data)
	}
	loaded, ok := d.loadIndexState(root)
	if !ok || loaded.Fingerprint != state.Fingerprint || !loaded.Complete {
		t.Fatalf("state did not round-trip: %+v, ok=%v", loaded, ok)
	}

	if err := os.WriteFile(path, []byte(`{"version":1,"workspace_root":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.loadIndexState(root); ok {
		t.Fatal("malformed state must not be trusted")
	}
	quarantined, err := filepath.Glob(path + ".v*.quarantine")
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("malformed state must be quarantined, got %v, err=%v", quarantined, err)
	}
}
