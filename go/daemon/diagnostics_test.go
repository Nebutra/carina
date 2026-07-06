package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPostEditDiagnostics: when an edit puts a file into a broken state, the
// patch observation must carry the checker's diagnostics (the self-correction
// feedback loop); a clean edit must stay quiet.
func TestPostEditDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit m.py")

	// Seed a valid module and read it so the write passes the provenance guard.
	if err := os.WriteFile(filepath.Join(ws, "m.py"), []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.executeAction(sess, task, &action{Tool: "read", Path: "m.py"})

	// A syntactically broken edit must surface diagnostics.
	broken := d.executeAction(sess, task, &action{Tool: "patch", Path: "m.py", Content: "def broken(:\n"})
	if !strings.Contains(broken, "diagnostics") {
		t.Fatalf("broken edit must surface diagnostics, got: %s", broken)
	}

	// A clean follow-up edit (provenance already recorded by the prior patch)
	// must NOT carry diagnostics.
	ok := d.executeAction(sess, task, &action{Tool: "patch", Path: "m.py", Content: "y = 2\n"})
	if strings.Contains(ok, "diagnostics") {
		t.Fatalf("clean edit must not surface diagnostics, got: %s", ok)
	}
}
