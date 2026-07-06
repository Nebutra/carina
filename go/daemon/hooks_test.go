package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreToolHookBlocks: a PreToolUse hook that exits 2 blocks the matching tool
// with its stderr as the feedback; non-matching tools are unaffected.
func TestPreToolHookBlocks(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	os.MkdirAll(filepath.Join(ws, ".carina"), 0o755)
	os.WriteFile(filepath.Join(ws, ".carina", "hooks.json"),
		[]byte(`[{"event":"PreToolUse","matcher":"read","command":["sh","-c","echo blocked-by-policy >&2; exit 2"]}]`), 0o644)
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hi\n"), 0o600)

	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "x")

	obs := d.executeAction(sess, task, &action{Tool: "read", Path: "a.txt"})
	if !strings.Contains(obs, "BLOCKED by hook") || !strings.Contains(obs, "blocked-by-policy") {
		t.Fatalf("read should be blocked by the PreToolUse hook, got: %s", obs)
	}

	// A tool the hook does not match is not blocked.
	obs = d.executeAction(sess, task, &action{Tool: "list"})
	if strings.Contains(obs, "BLOCKED") {
		t.Fatalf("list should not be blocked, got: %s", obs)
	}
}
