package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestLifecycleHooksRecordHealth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".carina"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(ws, "hook.out")
	raw, _ := json.Marshal([]HookSpec{{Event: "SessionStart", Command: []string{"sh", "-c", "cat > " + output}, TimeoutMS: 1000}})
	if err := os.WriteFile(filepath.Join(ws, ".carina", "hooks.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{hookOutcomes: make(map[string]hookOutcome)}
	d.runLifecycleHooks(ws, "SessionStart", map[string]any{"session_id": "sess_1"})
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"session_id":"sess_1"`) {
		t.Fatalf("payload = %s", got)
	}
	hook := loadHooks(ws)[0]
	outcome, ok := d.hookOutcome(hook)
	if !ok || !outcome.OK || outcome.Event != "SessionStart" {
		t.Fatalf("outcome = %#v, ok=%v", outcome, ok)
	}
}

func TestHookTimeoutAndStrictSchema(t *testing.T) {
	code, _, timedOut, _ := runHookCommand([]string{"sh", "-c", "sleep 1"}, nil, 10*time.Millisecond)
	if code == 0 || !timedOut {
		t.Fatalf("code=%d timedOut=%v", code, timedOut)
	}
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(`[{"event":"Unknown","command":["true"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if hooks := readHooks(path); len(hooks) != 0 {
		t.Fatalf("invalid event loaded: %#v", hooks)
	}
}
