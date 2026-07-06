package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanMode: while plan mode is on, exploration (list/read) is allowed but
// edits/commands are blocked; approving the plan lets them through.
func TestPlanMode(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("x\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "x")

	d.setPlanMode(sess.SessionID, true)

	// Read-only exploration is allowed in plan mode.
	if obs := d.executeAction(sess, task, &action{Tool: "read", Path: "a.txt"}); strings.Contains(obs, "plan mode") {
		t.Fatalf("read should be allowed in plan mode, got: %s", obs)
	}
	// Edits are blocked.
	obs := d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "y\n"})
	if !strings.Contains(obs, "plan mode") {
		t.Fatalf("patch should be blocked in plan mode, got: %s", obs)
	}
	// Commands are blocked.
	if obs := d.executeAction(sess, task, &action{Tool: "run", Command: []string{"echo", "hi"}}); !strings.Contains(obs, "plan mode") {
		t.Fatalf("run should be blocked in plan mode, got: %s", obs)
	}

	// Approving the plan lets edits through the plan gate.
	d.setPlanMode(sess.SessionID, false)
	obs = d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "y\n"})
	if strings.Contains(obs, "plan mode") {
		t.Fatalf("patch should pass the plan gate after approval, got: %s", obs)
	}
}
