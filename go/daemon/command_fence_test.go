package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/kernel"
)

func TestExecuteCommandWaitsForSessionExecutionFence(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	fence := d.sessionExecutionFence(sess.SessionID)
	fence.Lock()
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, err := d.executeCommand(sess.SessionID, "", []string{"sh", "-c", "printf fenced > command-fence.txt"}, &kernel.Decision{DecisionID: "decision-fence", Decision: "allowed"})
		done <- outcome{err: err}
	}()
	select {
	case result := <-done:
		fence.Unlock()
		t.Fatalf("command crossed restore fence early: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(workspace, "command-fence.txt")); !os.IsNotExist(err) {
		fence.Unlock()
		t.Fatalf("command mutated workspace while fenced: %v", err)
	}
	fence.Unlock()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command did not resume after fence release")
	}
	if raw, err := os.ReadFile(filepath.Join(workspace, "command-fence.txt")); err != nil || string(raw) != "fenced" {
		t.Fatalf("command result = %q, err=%v", raw, err)
	}
}
