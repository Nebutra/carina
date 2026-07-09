package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	// The three tools are read-only, so they are batchable.
	for _, tool := range []string{"code.search", "code.symbols", "code.map"} {
		if !isReadOnlyTool(tool) {
			t.Fatalf("%s must be read-only (batchable)", tool)
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
