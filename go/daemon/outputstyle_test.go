package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOutputStyleLoadedIntoPrompt: a project output-style.md is injected into
// the system prompt.
func TestOutputStyleLoadedIntoPrompt(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	os.MkdirAll(filepath.Join(ws, ".carina"), 0o755)
	os.WriteFile(filepath.Join(ws, ".carina", "output-style.md"), []byte("TERSE_TELEGRAPHIC_STYLE marker\n"), 0o644)

	cap := &capturingReasoner{}
	d.SetReasoner(cap)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "go")
	d.runTask(sess, task)

	if !strings.Contains(cap.lastPrompt, "TERSE_TELEGRAPHIC_STYLE marker") {
		t.Fatal("output-style.md should be injected into the prompt")
	}
	if !strings.Contains(cap.lastPrompt, "OUTPUT STYLE") {
		t.Fatal("output-style header missing")
	}
}
