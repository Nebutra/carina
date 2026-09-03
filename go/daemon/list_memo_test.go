package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListMemoSkipsSecondScan(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	if err := os.WriteFile(filepath.Join(ws, "keep.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "list twice")

	first := d.listWorkspaceOutcome(sess, task, nil)
	if first.status != "completed" || !strings.Contains(first.display, "keep.txt") {
		t.Fatalf("first list = %+v", first)
	}
	if got := d.listSearchZigCalls(); got != 1 {
		t.Fatalf("first list zig calls = %d, want 1", got)
	}
	second := d.listWorkspaceOutcome(sess, task, nil)
	if second.status != "completed" || second.display != first.display {
		t.Fatalf("memoized list = %+v want %q", second, first.display)
	}
	if got := d.listSearchZigCalls(); got != 1 {
		t.Fatalf("second list must not spawn carina-scan, zig calls = %d", got)
	}
	reads, memos := countListSearchFileReads(t, d, sess.SessionID, task.RunID)
	if reads < 2 {
		t.Fatalf("kernel FileRead must still be recorded on a memo hit, got %d", reads)
	}
	if memos < 1 {
		t.Fatal("memo hit must still record FileRead with memo=true")
	}
}

func TestSearchRejectsEmptyPattern(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "search")
	for _, pattern := range []string{"", "  ", "\t"} {
		got := d.searchWorkspaceOutcome(sess, task, pattern, nil)
		if got.status != "failed" || !strings.Contains(got.display, "non-empty pattern") {
			t.Fatalf("pattern %q = %+v", pattern, got)
		}
	}
	if got := d.listSearchZigCalls(); got != 0 {
		t.Fatalf("empty search must not spawn carina-grep, zig calls = %d", got)
	}
}

func TestSearchMemoSkipsSecondGrep(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	if err := os.WriteFile(filepath.Join(ws, "hit.go"), []byte("package hit\nconst MemoMarker = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "search twice")

	first := d.searchWorkspaceOutcome(sess, task, "MemoMarker", nil)
	if first.status != "completed" || !strings.Contains(first.display, "hit.go") {
		t.Fatalf("first search = %+v", first)
	}
	second := d.searchWorkspaceOutcome(sess, task, "MemoMarker", nil)
	if second.display != first.display {
		t.Fatalf("memoized search = %q want %q", second.display, first.display)
	}
	if got := d.listSearchZigCalls(); got != 1 {
		t.Fatalf("second search must not spawn carina-grep, zig calls = %d", got)
	}
	other := d.searchWorkspaceOutcome(sess, task, "OtherMarker", nil)
	if got := d.listSearchZigCalls(); got != 2 {
		t.Fatalf("a different pattern must miss the memo, zig calls = %d", got)
	}
	if strings.Contains(other.display, "hit.go") && other.display == first.display {
		t.Fatal("different pattern reused the memo")
	}
}

func TestListMemoInvalidatesOnWrite(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "list after write")

	first := d.listWorkspaceOutcome(sess, task, nil)
	if first.status != "completed" {
		t.Fatalf("first list = %+v", first)
	}
	repoRoot := repoRootFromHere(t)
	if _, err := os.Stat(filepath.Join(repoRoot, "zig/zig-out/bin/carina-patch-native")); err != nil {
		d.invalidateListSearchMemo(sess.SessionID)
	} else {
		d.dispatchAction(sess, task, &action{Tool: "read", Path: "a.txt"})
		obs := d.dispatchAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "two\n"})
		if !strings.Contains(obs, "applied") {
			t.Fatalf("patch should apply to invalidate the memo, got: %s", obs)
		}
	}
	second := d.listWorkspaceOutcome(sess, task, nil)
	if got := d.listSearchZigCalls(); got != 2 {
		t.Fatalf("write must force a rescan, zig calls = %d", got)
	}
	if second.status != "completed" {
		t.Fatalf("rescan list = %+v", second)
	}
}

func TestListMemoDisabledByEnv(t *testing.T) {
	t.Setenv("CARINA_LIST_MEMO", "0")
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "list")
	_ = d.listWorkspaceOutcome(sess, task, nil)
	_ = d.listWorkspaceOutcome(sess, task, nil)
	if got := d.listSearchZigCalls(); got != 2 {
		t.Fatalf("CARINA_LIST_MEMO=0 must spawn twice, zig calls = %d", got)
	}
}

func countListSearchFileReads(t *testing.T, d *Daemon, sessionID, runID string) (reads, memos int) {
	t.Helper()
	events, err := d.store.ReadEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != "FileRead" || event.TaskID != runID {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		reads++
		if memo, _ := payload["memo"].(bool); memo {
			memos++
		}
	}
	return reads, memos
}
