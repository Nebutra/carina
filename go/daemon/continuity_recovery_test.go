package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nebutra/carina/go/continuity"
)

func TestRecoveryEffectProofBlocksStartedUnknownTool(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "unsafe")
	effect := continuity.NewEffectContract(continuity.EffectUnknown, "")
	d.record(sess.SessionID, "ToolCallRequested", task.TaskID, "go", map[string]any{
		"call_id": "call_unsafe", "tool": "run", "kind": "command", "status": "pending", "arguments": map[string]any{}, "effect": effect,
	}, "")
	d.record(sess.SessionID, "ToolCallStarted", task.TaskID, "go", map[string]any{
		"call_id": "call_unsafe", "tool": "run", "kind": "command", "status": "running",
	}, "")
	effectSafe, externalSafe, _ := d.recoveryEffectProof(task)
	if effectSafe || externalSafe {
		t.Fatal("started unknown effect was considered recoverable")
	}
}

func TestWorkspaceAnchorDetectsDependencyDrift(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	path := filepath.Join(ws, "dependency.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.recordRead(sess.SessionID, "dependency.txt", "before")
	anchor, err := d.captureWorkspaceAnchor(sess)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := verifyWorkspaceAnchor(anchor); !ok {
		t.Fatalf("fresh anchor failed: %s", reason)
	}
	if err := os.WriteFile(path, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, _ := verifyWorkspaceAnchor(anchor); ok {
		t.Fatal("dependency drift was not detected")
	}
}

func TestWorkspaceAnchorRejectsDriftBeforeCheckpointAndSymlinkEscape(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	path := filepath.Join(ws, "dependency.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.recordRead(sess.SessionID, "dependency.txt", "before")
	if err := os.WriteFile(path, []byte("changed-before-checkpoint"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.captureWorkspaceAnchor(sess); err == nil {
		t.Fatal("drift between read and checkpoint was accepted")
	}

	d.readProvMu.Lock()
	d.readProv[sess.SessionID] = map[string]string{}
	d.readProvMu.Unlock()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	d.recordRead(sess.SessionID, "escape.txt", "outside")
	if _, err := d.captureWorkspaceAnchor(sess); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}
