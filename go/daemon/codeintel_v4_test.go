package daemon

// V4 tests (docs/plans/code-intelligence.md): the mtime staleness sweep
// (out-of-band edits reflected between code.* calls, empty-diff no-op, the
// CARINA_INDEX_SWEEP kill-switch, stat-only daemon behavior), LSP-sourced
// edge write-through (code.refs -> code.impact source: lsp), real rerank
// providers behind the V3 seam (BYOK adapters, CARINA_RERANK_MODEL
// no-fallback selection, the rerank deadline), and the D4
// invalidation-failure observability fix.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/toolchain"
)

// ---- A. mtime staleness sweep ------------------------------------------------

// TestSweepReflectsOutOfBandEdit: an edit made outside the runtime (no patch,
// no run command) must be visible to the next code.* call — the sweep diffs
// (mtime, size) stamps and routes changed paths through kernel.index.update.
func TestSweepReflectsOutOfBandEdit(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	path := filepath.Join(ws, "main.rs")
	os.WriteFile(path, []byte("pub fn zz_v4sw_one() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_one"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("initial build failed: %s", obs)
	}

	// Out-of-band edit (same byte length — the mtime must carry the diff).
	os.WriteFile(path, []byte("pub fn zz_v4sw_two() {}\n"), 0o600)
	future := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_two"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("the sweep must reflect an out-of-band edit, got: %s", obs)
	}
	stale := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_one"})
	if strings.Contains(stale, "main.rs") {
		t.Fatalf("stale pre-edit content must be gone after the sweep, got: %s", stale)
	}
}

// TestSweepReflectsSameSecondSameSizeEdit: an out-of-band edit whose
// (Unix-second mtime, size) stamp matches the snapshot — an editor save
// landing in the same second as the build's stat pass — must still be
// visible: the sweep keys stamps on nanosecond mtimes, so second-granularity
// aliasing can never serve stale rows forever (full-sync semantics).
func TestSweepReflectsSameSecondSameSizeEdit(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	path := filepath.Join(ws, "main.rs")
	os.WriteFile(path, []byte("pub fn zz_v4ss_one() {}\n"), 0o600)
	// Pin v1 to a known past instant so both versions can share the same Unix
	// second while their nanoseconds differ.
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	v1 := base.Add(111 * time.Millisecond)
	if err := os.Chtimes(path, v1, v1); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4ss_one"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("initial build failed: %s", obs)
	}

	// Same byte length, same Unix second, later nanoseconds: a (seconds, size)
	// stamp is blind to this edit; a nanosecond stamp is not.
	os.WriteFile(path, []byte("pub fn zz_v4ss_two() {}\n"), 0o600)
	v2 := base.Add(222 * time.Millisecond)
	if err := os.Chtimes(path, v2, v2); err != nil {
		t.Fatal(err)
	}

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4ss_two"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("a same-second same-size out-of-band edit must be swept, got: %s", obs)
	}
	stale := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4ss_one"})
	if strings.Contains(stale, "main.rs") {
		t.Fatalf("stale pre-edit content must be gone after the sweep, got: %s", stale)
	}
}

// TestSweepRacyStampIsRecheckedNotTrusted: a snapshot stamp whose mtime is
// not strictly older than the stat pass that captured it cannot prove the
// content unchanged — a write landing on the very same timestamp afterwards
// would be invisible even at nanosecond precision. Git's racy-clean rule
// applies: such paths are always re-sent to kernel.index.update (content-hash
// keyed, so the redundant update is cheap) instead of being trusted.
func TestSweepRacyStampIsRecheckedNotTrusted(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	path := filepath.Join(ws, "main.rs")
	os.WriteFile(path, []byte("pub fn zz_v4racy_one() {}\n"), 0o600)
	// A stamp from the future is never strictly older than the scan that
	// captured it: the worst-case racy stamp, reproducible bit-identically.
	racy := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, racy, racy); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4racy_one"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("initial build failed: %s", obs)
	}

	// Same byte length and the IDENTICAL (nanosecond) mtime: only the
	// racy-stamp rule can carry this edit.
	os.WriteFile(path, []byte("pub fn zz_v4racy_two() {}\n"), 0o600)
	if err := os.Chtimes(path, racy, racy); err != nil {
		t.Fatal(err)
	}

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4racy_two"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("a racy-stamp edit must be swept, got: %s", obs)
	}
	stale := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4racy_one"})
	if strings.Contains(stale, "main.rs") {
		t.Fatalf("stale pre-edit content must be gone after the sweep, got: %s", stale)
	}
}

// TestSweepReflectsRemovalAndAddition: vanished paths become deletes, new
// supported files enter via the existing update path.
func TestSweepReflectsRemovalAndAddition(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "a.rs"), []byte("pub fn zz_v4sw_gone() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_gone"}); !strings.Contains(obs, "a.rs") {
		t.Fatalf("initial build failed: %s", obs)
	}

	os.Remove(filepath.Join(ws, "a.rs"))
	os.WriteFile(filepath.Join(ws, "b.rs"), []byte("pub fn zz_v4sw_added() {}\n"), 0o600)

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_added"})
	if !strings.Contains(obs, "b.rs") {
		t.Fatalf("the sweep must pick up an out-of-band added file, got: %s", obs)
	}
	stale := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_gone"})
	if strings.Contains(stale, "a.rs") {
		t.Fatalf("a vanished file's rows must be dropped by the sweep, got: %s", stale)
	}
}

// TestSweepEmptyDiffMakesNoKernelUpdateCall: an unchanged tree must not cost
// a kernel.index.update round trip (stat + map diff only).
func TestSweepEmptyDiffMakesNoKernelUpdateCall(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v4sw_idle() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_idle"})
	d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_idle"})

	if payloads := v3StatusEventPayloads(t, d, sess.SessionID, "index_update_completed"); len(payloads) != 0 {
		t.Fatalf("an empty sweep diff must make no kernel.index.update call, got: %v", payloads)
	}
}

// TestSweepEnvKillSwitchSkipsTheSweep: CARINA_INDEX_SWEEP=off disables the
// sweep for perf-sensitive setups (default is on) — out-of-band edits then
// stay invisible until the existing invalidation mechanisms run.
func TestSweepEnvKillSwitchSkipsTheSweep(t *testing.T) {
	t.Setenv("CARINA_INDEX_SWEEP", "off")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	path := filepath.Join(ws, "main.rs")
	os.WriteFile(path, []byte("pub fn zz_v4sw_off_one() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_off_one"}); !strings.Contains(obs, "main.rs") {
		t.Fatalf("initial build failed: %s", obs)
	}
	os.WriteFile(path, []byte("pub fn zz_v4sw_off_two() {}\n"), 0o600)
	future := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	fresh := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_off_two"})
	if strings.Contains(fresh, "main.rs") {
		t.Fatalf("CARINA_INDEX_SWEEP=off must skip the sweep, got: %s", fresh)
	}
	stale := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4sw_off_one"})
	if !strings.Contains(stale, "main.rs") {
		t.Fatalf("with the sweep off the index keeps its last-synced rows, got: %s", stale)
	}
}

// TestSweepIsStatOnlyInTheDaemon: the daemon never reads file bytes for the
// sweep — a stat-able but unreadable changed file must not break it (content
// re-reads happen kernel-side under FileRead, where a read failure keeps the
// indexed rows, V2 D1), and readable siblings still sync.
func TestSweepIsStatOnlyInTheDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	pathA := filepath.Join(ws, "a.rs")
	pathB := filepath.Join(ws, "b.rs")
	os.WriteFile(pathA, []byte("pub fn zz_v4stat_a1() {}\n"), 0o600)
	os.WriteFile(pathB, []byte("pub fn zz_v4stat_b1() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4stat_a1"}); !strings.Contains(obs, "a.rs") {
		t.Fatalf("initial build failed: %s", obs)
	}

	future := time.Now().Add(3 * time.Second)
	os.WriteFile(pathA, []byte("pub fn zz_v4stat_a2() {}\n"), 0o600)
	os.Chtimes(pathA, future, future)
	os.WriteFile(pathB, []byte("pub fn zz_v4stat_b2() {}\n"), 0o600)
	os.Chtimes(pathB, future, future)
	// b.rs stays stat-able but unreadable: a content-reading sweep would fail
	// here; a stat-only sweep proceeds and lets the kernel gate the read.
	if err := os.Chmod(pathB, 0o200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(pathB, 0o644) })

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4stat_a2"})
	if !strings.Contains(obs, "a.rs") {
		t.Fatalf("an unreadable sibling must not stop the sweep, got: %s", obs)
	}
	kept := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4stat_b1"})
	if !strings.Contains(kept, "b.rs") {
		t.Fatalf("kernel-side read failure keeps the indexed rows (D1), got: %s", kept)
	}
}

// TestSweepReflectsReadabilityChange: a file that was stat-able but
// unreadable at build time (the kernel's read failed, so no rows were ever
// ingested) must still enter the index once `chmod +r` restores readability.
// chmod changes ctime only — mtime and size stay identical to the snapshot —
// so the stamp must also carry the stat mode bits (still one os.Stat, still
// metadata only) for the sweep to converge the index to what is on disk and
// readable.
func TestSweepReflectsReadabilityChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	pathA := filepath.Join(ws, "a.rs")
	pathB := filepath.Join(ws, "b.rs")
	os.WriteFile(pathA, []byte("pub fn zz_v4perm_a() {}\n"), 0o600)
	os.WriteFile(pathB, []byte("pub fn zz_v4perm_b() {}\n"), 0o600)
	// Pin mtimes to the past so the racy-stamp rule cannot mask the mode diff.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(pathA, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pathB, past, past); err != nil {
		t.Fatal(err)
	}
	// b.rs is stat-able (directory exec bit) but unreadable at build time.
	if err := os.Chmod(pathB, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(pathB, 0o644) })
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4perm_a"}); !strings.Contains(obs, "a.rs") {
		t.Fatalf("initial build failed: %s", obs)
	}
	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4perm_b"}); strings.Contains(obs, "b.rs") {
		t.Fatalf("fixture broken: an unreadable file must not be ingested at build, got: %s", obs)
	}

	// chmod +r flips ctime only: mtime and size still match the snapshot.
	if err := os.Chmod(pathB, 0o644); err != nil {
		t.Fatal(err)
	}

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4perm_b"})
	if !strings.Contains(obs, "b.rs") {
		t.Fatalf("a readability change (chmod +r) must converge the index, got: %s", obs)
	}
}

// TestSweepFailureIsObservableAndHeals: a failed sweep kernel.index.update is
// never swallowed — one log line, an index_sweep_failed audit event (reason
// only), and a cleared indexBuilt flag so the next code.* call heals with a
// full build (the invalidateIndex D4 contract, sweep half).
func TestSweepFailureIsObservableAndHeals(t *testing.T) {
	d, ws, stateDir := newV4DaemonWithState(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v4swf_marker() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// Simulate a session whose index was built in a previous daemon life
	// (indexBuilt set, no snapshot — every path diffs as changed) with the
	// kernel-side database path obstructed by a directory: the sweep's
	// kernel.index.update fails to open it.
	sum := sha256.Sum256([]byte(ws))
	dbPath := filepath.Join(stateDir, "index", fmt.Sprintf("%x.sqlite", sum))
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	d.indexBuilt.Store(sess.SessionID, true)

	out := v3CaptureStdout(t, func() {
		d.sweepIndex(sess, task)
	})
	if !strings.Contains(out, "index sweep failed") {
		t.Fatalf("missing the sweep-failure log line, got: %q", out)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "index_sweep_failed")
	if len(payloads) == 0 {
		t.Fatal("a failed sweep must record an index_sweep_failed audit event")
	}
	if payloads[0]["reason"] != "kernel-error" {
		t.Fatalf("the event must classify the reason (never content), got: %v", payloads[0])
	}
	if _, still := d.indexBuilt.Load(sess.SessionID); still {
		t.Fatal("a failed sweep must clear indexBuilt so the next code.* call heals")
	}

	// Heal: with the obstruction removed, the next code.* call rebuilds.
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4swf_marker"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("the next code.* call must heal with a full build, got: %s", obs)
	}
}

// TestSweepScanFailureIsObservable: the sweep's other failure branch — the
// Zig scanner itself fails hard (crash, kill, no output) — surfaces through
// the same mechanism with reason scan-error.
func TestSweepScanFailureIsObservable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script scanner stub")
	}
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")
	d.indexBuilt.Store(sess.SessionID, true)
	// A scanner that dies with no stdout is a hard toolchain failure.
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "carina-scan"),
		[]byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	realTools := d.tools
	d.tools = toolchain.New(stubDir)
	defer func() { d.tools = realTools }()

	out := v3CaptureStdout(t, func() {
		d.sweepIndex(sess, task)
	})
	if !strings.Contains(out, "index sweep failed") {
		t.Fatalf("missing the sweep-failure log line, got: %q", out)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "index_sweep_failed")
	if len(payloads) == 0 {
		t.Fatal("a failed scan must record an index_sweep_failed audit event")
	}
	if payloads[0]["reason"] != "scan-error" {
		t.Fatalf("the event must classify the reason as scan-error, got: %v", payloads[0])
	}
	if _, still := d.indexBuilt.Load(sess.SessionID); still {
		t.Fatal("a failed scan must clear indexBuilt so the next code.* call heals")
	}
}

// ---- B. LSP-sourced edges: write-through + impact labeling -------------------

// TestFakeLSPRefsServerHelperV4 is not a real test: re-exec'ed with
// CARINA_FAKE_LSP_V4=1 it acts as a language server whose references answer
// points at the caller's call site (caller.rs line 2) inside the request's
// directory — the precise relation the write-through must persist.
func TestFakeLSPRefsServerHelperV4(t *testing.T) {
	if os.Getenv("CARINA_FAKE_LSP_V4") != "1" {
		t.Skip("helper process, spawned by TestCodeRefsPersistsLSPEdgesForImpactSource")
	}
	runFakeLSPRefsServerV4(os.Stdin, os.Stdout)
	os.Exit(0)
}

func runFakeLSPRefsServerV4(r io.Reader, w io.Writer) {
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
		case "textDocument/references", "textDocument/definition":
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
			caller := filepath.Join(filepath.Dir(path), "caller.rs")
			echo := (&url.URL{Scheme: "file", Path: caller}).String()
			// 0-based line 1, char 4: the call site inside zz_v4edge_caller.
			rng := map[string]any{"start": map[string]int{"line": 1, "character": 4}}
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

// TestCodeRefsPersistsLSPEdgesForImpactSource: a successful fake-LSP
// code.refs opportunistically persists precise edges (write-through, no
// full-workspace crawl), and a following code.impact reports source: lsp
// with the deduped 1.0 confidence.
func TestCodeRefsPersistsLSPEdgesForImpactSource(t *testing.T) {
	t.Setenv("CARINA_FAKE_LSP_V4", "1")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	prev := lspServerForExt
	lspServerForExt = func(ext string) (semanticServer, bool) {
		return semanticServer{bin: os.Args[0], args: []string{"-test.run=^TestFakeLSPRefsServerHelperV4$"}, langID: "rust"}, true
	}
	defer func() { lspServerForExt = prev }()

	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v4edge_target() {}\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "caller.rs"),
		[]byte("pub fn zz_v4edge_caller() {\n    zz_v4edge_target();\n}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.refs", Name: "zz_v4edge_target"})
	if !strings.Contains(obs, "precision:lsp") {
		t.Fatalf("fixture broken: the fake LSP path must answer, got: %s", obs)
	}
	if !strings.Contains(obs, "caller.rs") {
		t.Fatalf("fixture broken: the reference must point at caller.rs, got: %s", obs)
	}

	impact := d.executeAction(sess, task, &action{Tool: "code.impact", Name: "zz_v4edge_target"})
	if !strings.Contains(impact, "source: lsp") {
		t.Fatalf("after an LSP write-through, code.impact must report source: lsp, got: %s", impact)
	}
	if !strings.Contains(impact, "zz_v4edge_caller") {
		t.Fatalf("the caller must be a dependent, got: %s", impact)
	}
	if !strings.Contains(impact, "confidence 1.00") {
		t.Fatalf("the lsp edge carries confidence 1.0, got: %s", impact)
	}
}

// TestCodeImpactWithoutLSPEdgesKeepsTreeSitterLabel: with no persisted lsp
// edges the impact header stays honestly tree-sitter (the V3 tests' shape).
func TestCodeImpactWithoutLSPEdgesKeepsTreeSitterLabel(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	os.WriteFile(filepath.Join(ws, "h.rs"), []byte("pub fn zz_v4ts_only() {}\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "c.rs"),
		[]byte("pub fn zz_v4ts_caller() {\n    zz_v4ts_only();\n}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	obs := d.executeAction(sess, task, &action{Tool: "code.impact", Name: "zz_v4ts_only"})
	if !strings.Contains(obs, "source: tree-sitter") {
		t.Fatalf("without lsp edges the impact label stays tree-sitter, got: %s", obs)
	}
}

// ---- C. real rerank providers behind the V3 seam -----------------------------

// v4FakeRerankProvider implements modelrouter.RerankProvider: a deterministic
// reverse ordering, optional blocking (deadline tests), call recording.
type v4FakeRerankProvider struct {
	name     string
	calls    int
	block    bool
	partial  bool // answer with fewer (index, score) pairs than documents
	lastDocs []string
}

func (f *v4FakeRerankProvider) Name() string { return f.name }

func (f *v4FakeRerankProvider) Rerank(ctx context.Context, req modelrouter.RerankRequest) (*modelrouter.RerankResponse, error) {
	f.calls++
	f.lastDocs = append([]string(nil), req.Documents...)
	if f.block {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
			return nil, errors.New("hang guard tripped: no deadline was applied")
		}
	}
	results := make([]modelrouter.RerankResult, len(req.Documents))
	for i := range req.Documents {
		results[i] = modelrouter.RerankResult{Index: len(req.Documents) - 1 - i, Score: 1 - float64(i)/10}
	}
	if f.partial && len(results) > 1 {
		results = results[:1] // top pair only: a subset answer, not an error
	}
	return &modelrouter.RerankResponse{Provider: f.name, Model: req.Model, Results: results}, nil
}

// TestRerankModelRoutesOrderingToSelectedProvider: CARINA_RERANK_MODEL names
// a registered provider — the header says rerank:<provider> and only result
// ORDERING changes (snippets/provenance untouched).
func TestRerankModelRoutesOrderingToSelectedProvider(t *testing.T) {
	d, sess, task, baseline := v3RerankFixture(t)
	fake := &v4FakeRerankProvider{name: "fake-rr"}
	d.router.RegisterRerankProvider(fake)
	t.Setenv("CARINA_RERANK_MODEL", "fake-rr/rerank-x")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	if !strings.Contains(v3FirstLine(obs), "rerank:fake-rr") {
		t.Fatalf("the header must name the selected provider, got: %s", v3FirstLine(obs))
	}
	got := v3HitPaths(obs)
	want := []string{baseline[1], baseline[0]}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("provider ordering must apply: got %v, want %v (obs: %s)", got, want, obs)
	}
	if fake.calls == 0 {
		t.Fatal("the selected provider must be consulted")
	}
	if len(fake.lastDocs) != 2 {
		t.Fatalf("the provider sees one document per hit, got %d", len(fake.lastDocs))
	}
	// Ordering-only: both snippets still render (never dropped or rewritten).
	if !strings.Contains(obs, "zz_v3rr_alpha") || !strings.Contains(obs, "zz_v3rr_beta") {
		t.Fatalf("snippets must be untouched, got: %s", obs)
	}
}

// TestRerankPartialProviderResultsPadWithKernelOrder: a provider answering
// fewer (index, score) pairs than documents (dropped/deduped/over-limit) is
// a successful partial ordering, not an error — the adapter pads the
// permutation with the unreturned indices in kernel order (V4 §C), so the
// paid answer applies and nothing degrades to rerank:off(rerank-error).
func TestRerankPartialProviderResultsPadWithKernelOrder(t *testing.T) {
	d, sess, task, baseline := v3RerankFixture(t)
	fake := &v4FakeRerankProvider{name: "fake-rr", partial: true}
	d.router.RegisterRerankProvider(fake)
	t.Setenv("CARINA_RERANK_MODEL", "fake-rr/rerank-x")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	if !strings.Contains(v3FirstLine(obs), "rerank:fake-rr") {
		t.Fatalf("a partial provider answer is a success, not a degrade, got: %s", v3FirstLine(obs))
	}
	got := v3HitPaths(obs)
	// The provider returned only its top pick (the reversal's first index);
	// the unreturned hit follows in kernel order.
	want := []string{baseline[1], baseline[0]}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("partial ordering must apply with kernel-order padding: got %v, want %v (obs: %s)", got, want, obs)
	}
	if payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_rerank_degraded"); len(payloads) != 0 {
		t.Fatalf("a healthy partial answer must not record a degrade event, got: %v", payloads)
	}
}

// TestRerankModelUnregisteredPrefixNeverFallsBack: content egress goes only
// to the provider the user named — an unregistered prefix (unknown provider,
// missing key, offline) keeps the stage off with rerank:off(no-provider) +
// a code_rerank_degraded audit event, and NEVER routes snippets elsewhere.
func TestRerankModelUnregisteredPrefixNeverFallsBack(t *testing.T) {
	d, sess, task, baseline := v3RerankFixture(t)
	fake := &v4FakeRerankProvider{name: "fake-rr"}
	d.router.RegisterRerankProvider(fake)
	t.Setenv("CARINA_RERANK_MODEL", "cohere/rerank-english-v3.0")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	if !strings.Contains(v3FirstLine(obs), "rerank:off(no-provider)") {
		t.Fatalf("an unregistered prefix must degrade observably, got: %s", v3FirstLine(obs))
	}
	got := v3HitPaths(obs)
	if len(got) != 2 || got[0] != baseline[0] || got[1] != baseline[1] {
		t.Fatalf("the kernel order must be untouched, got %v want %v", got, baseline)
	}
	if fake.calls != 0 {
		t.Fatalf("snippets must never be routed to a provider the user did not select (calls=%d)", fake.calls)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "code_rerank_degraded")
	found := false
	for _, p := range payloads {
		if p["reason"] == "no-provider" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a code_rerank_degraded event with reason no-provider, got: %v", payloads)
	}
}

// TestRerankModelUnsetLeavesTheStageBitIdenticalOff: unset means no header
// segment, no provider call — the V3 rendering, bit-identical.
func TestRerankModelUnsetLeavesTheStageBitIdenticalOff(t *testing.T) {
	d, sess, task, baseline := v3RerankFixture(t)
	fake := &v4FakeRerankProvider{name: "fake-rr"}
	d.router.RegisterRerankProvider(fake)
	t.Setenv("CARINA_RERANK_MODEL", "")

	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	if strings.Contains(v3FirstLine(obs), "rerank") {
		t.Fatalf("with no selection the header must not mention rerank, got: %s", v3FirstLine(obs))
	}
	got := v3HitPaths(obs)
	if len(got) != 2 || got[0] != baseline[0] || got[1] != baseline[1] {
		t.Fatalf("kernel order must be untouched, got %v want %v", got, baseline)
	}
	if fake.calls != 0 {
		t.Fatalf("no provider may be consulted without explicit selection (calls=%d)", fake.calls)
	}
}

// TestRerankDeadlineFallsBackToKernelOrder: a hanging provider is cut by the
// rerank deadline — un-reranked kernel order, rerank:off(rerank-error), and
// the audit event, well before the hang guard.
func TestRerankDeadlineFallsBackToKernelOrder(t *testing.T) {
	d, sess, task, baseline := v3RerankFixture(t)
	fake := &v4FakeRerankProvider{name: "hang-rr", block: true}
	d.router.RegisterRerankProvider(fake)
	t.Setenv("CARINA_RERANK_MODEL", "hang-rr/rerank-x")
	oldTimeout := rerankTimeout
	rerankTimeout = 150 * time.Millisecond
	defer func() { rerankTimeout = oldTimeout }()

	start := time.Now()
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v3rr_needle"})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the rerank stage must run under a deadline, took %s", elapsed)
	}
	if !strings.Contains(v3FirstLine(obs), "rerank:off(rerank-error)") {
		t.Fatalf("a timed-out rerank must degrade observably, got: %s", v3FirstLine(obs))
	}
	got := v3HitPaths(obs)
	if len(got) != 2 || got[0] != baseline[0] || got[1] != baseline[1] {
		t.Fatalf("timeout must fall back to the kernel order, got %v want %v", got, baseline)
	}
	if len(v3StatusEventPayloads(t, d, sess.SessionID, "code_rerank_degraded")) == 0 {
		t.Fatal("a rerank timeout must record a code_rerank_degraded audit event")
	}
}

// TestRerankRegistrationRequiresCredential: rerank backends register only
// when a credential resolves, and never in offline mode — the embeddings
// delta, verbatim.
func TestRerankRegistrationRequiresCredential(t *testing.T) {
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("COHERE_API_KEY", "")
	bare := modelrouter.New()
	registerRerankProviders(bare, false, store)
	if bare.HasRerankProvider() {
		t.Fatal("without credentials no rerank provider may register")
	}

	t.Setenv("VOYAGE_API_KEY", "sk-rr-test")
	keyed := modelrouter.New()
	registerRerankProviders(keyed, false, store)
	if !keyed.HasRerankProviderNamed("voyage") {
		t.Fatal("a resolvable VOYAGE_API_KEY must register the voyage rerank provider")
	}
	if keyed.HasRerankProviderNamed("cohere") {
		t.Fatal("cohere must not register without its key")
	}

	offline := modelrouter.New()
	registerRerankProviders(offline, true, store)
	if offline.HasRerankProvider() {
		t.Fatal("offline mode must not register rerank providers")
	}
}

// TestVoyageRerankAdapter: POST /v1/rerank with the documented voyage wire
// shape, Bearer credential, return_documents=false; results pass through in
// provider order.
func TestVoyageRerankAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-voyage-rr" {
			t.Fatalf("authorization header = %q", got)
		}
		var body struct {
			Model           string   `json:"model"`
			Query           string   `json:"query"`
			Documents       []string `json:"documents"`
			TopK            int      `json:"top_k"`
			ReturnDocuments *bool    `json:"return_documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "rerank-2" || body.Query != "find dispatch" {
			t.Fatalf("bad body: %+v", body)
		}
		if len(body.Documents) != 3 || body.Documents[2] != "d2" {
			t.Fatalf("bad documents: %+v", body.Documents)
		}
		if body.TopK != 3 {
			t.Fatalf("top_k = %d, want 3", body.TopK)
		}
		if body.ReturnDocuments == nil || *body.ReturnDocuments {
			t.Fatalf("return_documents must be present and false: %+v", body.ReturnDocuments)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"index":2,"relevance_score":0.92},{"index":0,"relevance_score":0.41}],"model":"rerank-2","usage":{"total_tokens":21}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("voyage", "sk-voyage-rr", nil); err != nil {
		t.Fatal(err)
	}
	p := &voyageRerankProvider{providerBase: providerBase{
		id: "voyage", baseURL: srv.URL + "/v1", defaultModel: "rerank-2",
		auth: auth.ProviderChain("voyage", nil, store, nil), client: srv.Client(),
	}}

	resp, err := p.Rerank(context.Background(), modelrouter.RerankRequest{
		Query: "find dispatch", Documents: []string{"d0", "d1", "d2"}, TopK: 3,
	})
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if resp.Provider != "voyage" || resp.Model != "rerank-2" {
		t.Fatalf("bad response identity: %+v", resp)
	}
	if len(resp.Results) != 2 ||
		resp.Results[0].Index != 2 || resp.Results[0].Score != 0.92 ||
		resp.Results[1].Index != 0 || resp.Results[1].Score != 0.41 {
		t.Fatalf("results must pass through in provider order: %+v", resp.Results)
	}
}

// TestCohereRerankAdapter: POST /v2/rerank with the documented cohere v3
// wire shape (top_n, results[].relevance_score).
func TestCohereRerankAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/rerank" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-cohere-rr" {
			t.Fatalf("authorization header = %q", got)
		}
		var body struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      int      `json:"top_n"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "rerank-english-v3.0" || body.Query != "find dispatch" {
			t.Fatalf("bad body: %+v", body)
		}
		if len(body.Documents) != 2 || body.TopN != 2 {
			t.Fatalf("bad documents/top_n: %+v", body)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"id":"rr-1","results":[{"index":1,"relevance_score":0.88},{"index":0,"relevance_score":0.12}],"meta":{"billed_units":{"search_units":1}}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("cohere", "sk-cohere-rr", nil); err != nil {
		t.Fatal(err)
	}
	p := &cohereRerankProvider{providerBase: providerBase{
		id: "cohere", baseURL: srv.URL + "/v2", defaultModel: "rerank-english-v3.0",
		auth: auth.ProviderChain("cohere", nil, store, nil), client: srv.Client(),
	}}

	resp, err := p.Rerank(context.Background(), modelrouter.RerankRequest{
		Query: "find dispatch", Documents: []string{"d0", "d1"}, TopK: 2,
	})
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if resp.Provider != "cohere" || resp.Model != "rerank-english-v3.0" {
		t.Fatalf("bad response identity: %+v", resp)
	}
	if len(resp.Results) != 2 ||
		resp.Results[0].Index != 1 || resp.Results[0].Score != 0.88 ||
		resp.Results[1].Index != 0 || resp.Results[1].Score != 0.12 {
		t.Fatalf("results must pass through in provider order: %+v", resp.Results)
	}
}

// ---- C2. embeddings deadline (final closure) ----------------------------------

// v4BlockingEmbedder is a healthy 2-dim embeddings fake until block flips: it
// then parks on the call's context — a deadline cuts it in milliseconds, and
// a missing deadline trips the hang guard (well past the asserted bound).
type v4BlockingEmbedder struct {
	name  string
	dims  int
	block bool
	calls int
}

func (f *v4BlockingEmbedder) Name() string { return f.name }

func (f *v4BlockingEmbedder) Embed(ctx context.Context, req modelrouter.EmbeddingsRequest) (*modelrouter.EmbeddingsResponse, error) {
	f.calls++
	if f.block {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Second):
			return nil, errors.New("hang guard tripped: no deadline was applied")
		}
	}
	vectors := make([][]float32, len(req.Inputs))
	for i := range req.Inputs {
		vectors[i] = make([]float32, f.dims)
		vectors[i][0] = 1
	}
	return &modelrouter.EmbeddingsResponse{
		Provider: f.name, Model: "unit-embed-model", Vectors: vectors, InputTokens: len(req.Inputs),
	}, nil
}

// TestEmbedQueryDeadlineFallsBackToKeywordOnly: a hanging embeddings endpoint
// at query time is cut by the embed deadline — keyword-only results with
// semantic:off(provider-error), well before providerHTTPTimeout (the rerank
// deadline treatment, embeddings half).
func TestEmbedQueryDeadlineFallsBackToKeywordOnly(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	fake := &v4BlockingEmbedder{name: "fake-embed", dims: 2}
	d.router.RegisterEmbeddingsProvider(fake)
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v4edl_query() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")
	oldTimeout := embedTimeout
	embedTimeout = 150 * time.Millisecond
	defer func() { embedTimeout = oldTimeout }()

	// First search: healthy sync + healthy query embed.
	if obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4edl_query"}); v3FirstLine(obs) != "channels: keyword:on semantic:on" {
		t.Fatalf("healthy search failed: %s", obs)
	}
	// The provider hangs at query time.
	fake.block = true
	start := time.Now()
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4edl_query"})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the query embed must run under a deadline, took %s", elapsed)
	}
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(provider-error)" {
		t.Fatalf("first line = %q, want %q", got, "channels: keyword:on semantic:off(provider-error)")
	}
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("keyword hits must survive a hanging provider, got: %s", obs)
	}
}

// TestEmbedSyncDeadlineDegradesInsteadOfStalling: the batch embeds inside
// syncEmbeddings run under the same deadline — a provider that hangs from the
// first sync degrades the tool call in milliseconds (observable via the
// embedding_sync_failed event) instead of stalling it for providerHTTPTimeout.
func TestEmbedSyncDeadlineDegradesInsteadOfStalling(t *testing.T) {
	t.Setenv("CARINA_EMBEDDINGS_MODEL", "fake-embed/unit-embed-model")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	fake := &v4BlockingEmbedder{name: "fake-embed", dims: 2, block: true}
	d.router.RegisterEmbeddingsProvider(fake)
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v4edl_sync() {}\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")
	oldTimeout := embedTimeout
	embedTimeout = 150 * time.Millisecond
	defer func() { embedTimeout = oldTimeout }()

	start := time.Now()
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4edl_sync"})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("sync embeds must run under a deadline, took %s", elapsed)
	}
	if got := v3FirstLine(obs); got != "channels: keyword:on semantic:off(provider-error)" {
		t.Fatalf("first line = %q, want %q", got, "channels: keyword:on semantic:off(provider-error)")
	}
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("keyword hits must survive a hanging provider, got: %s", obs)
	}
	if fake.calls == 0 {
		t.Fatal("the provider must have been consulted (and cut) by the sync")
	}
	if len(v3StatusEventPayloads(t, d, sess.SessionID, "embedding_sync_failed")) == 0 {
		t.Fatal("a timed-out sync must record an embedding_sync_failed audit event")
	}
}

// ---- D4. invalidation-failure observability (daemon half) --------------------

// newV4DaemonWithState mirrors newLoopDaemon but keeps the state dir in hand
// so a test can sabotage the kernel-side index database path.
func newV4DaemonWithState(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	ws := t.TempDir()
	stateDir := t.TempDir()
	d, err := New(Options{StateDir: stateDir, KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	return d, ws, stateDir
}

// TestInvalidateIndexFailureIsObservableAndHeals: a failed post-write index
// update is no longer '_, _ =' swallowed — one log line, an
// index_invalidation_failed audit event (reason only), and a cleared
// indexBuilt flag so the next code.* call heals with a full build.
func TestInvalidateIndexFailureIsObservableAndHeals(t *testing.T) {
	d, ws, stateDir := newV4DaemonWithState(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "explore")

	// Simulate a session whose index was built in a previous daemon life,
	// with the kernel-side database path obstructed by a directory: the next
	// kernel.index.update fails to open it.
	sum := sha256.Sum256([]byte(ws))
	dbPath := filepath.Join(stateDir, "index", fmt.Sprintf("%x.sqlite", sum))
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	d.indexBuilt.Store(sess.SessionID, true)

	out := v3CaptureStdout(t, func() {
		d.invalidateIndex(sess.SessionID, []string{"main.rs"})
	})
	if !strings.Contains(out, "index invalidation failed") {
		t.Fatalf("missing the invalidation-failure log line, got: %q", out)
	}
	payloads := v3StatusEventPayloads(t, d, sess.SessionID, "index_invalidation_failed")
	if len(payloads) == 0 {
		t.Fatal("a failed invalidation must record an index_invalidation_failed audit event")
	}
	if payloads[0]["reason"] != "kernel-error" {
		t.Fatalf("the event must classify the reason (never content), got: %v", payloads[0])
	}
	if _, still := d.indexBuilt.Load(sess.SessionID); still {
		t.Fatal("a failed invalidation must clear indexBuilt so the next code.* call heals")
	}

	// Heal: with the obstruction removed, the next code.* call rebuilds.
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws, "main.rs"), []byte("pub fn zz_v4heal_marker() {}\n"), 0o600)
	obs := d.executeAction(sess, task, &action{Tool: "code.search", Query: "zz_v4heal_marker"})
	if !strings.Contains(obs, "main.rs") {
		t.Fatalf("the next code.* call must heal with a full build, got: %s", obs)
	}
}
