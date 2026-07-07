package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func newLoopDaemon(t *testing.T) (*Daemon, string) {
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
	d, err := New(Options{StateDir: t.TempDir(), KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	return d, ws
}

// TestLoopRequeryOnMalformedAction: the model emits junk, then a valid done;
// the junk must be re-queried without consuming a real turn.
func TestLoopRequeryOnMalformedAction(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{
		"I'll think about it... (no json here)",     // malformed -> requery
		"still thinking, no action",                 // malformed -> requery
		`{"tool":"done","summary":"nothing to do"}`, // valid
	}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "noop")
	d.runTask(sess, task)

	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("requery should recover and complete, got %s", tk.Status)
	}
}

// TestLoopGracefulDegradeOnMaxTurns: the model never says done and just keeps
// reading; the task must degrade (not hard-fail) and report partial state.
func TestLoopGracefulDegradeOnMaxTurns(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hi\n"), 0o600)
	// Always read the same file, never finish.
	steps := make([]string, 40)
	for i := range steps {
		steps[i] = `{"tool":"read","path":"a.txt"}`
	}
	d.SetReasoner(&scriptedReasoner{steps: steps})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "loop forever")
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.TaskID)
	if tk.Status != "degraded" {
		t.Fatalf("runaway loop should degrade gracefully, got %s", tk.Status)
	}
}

// TestLoopReasonerRetry: a flaky reasoner that fails a few times then
// succeeds must not kill the task.
func TestLoopReasonerRetry(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 5 * 1e6 // 5ms for the test
	defer func() { retryBaseDelay = old }()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&flakyReasoner{failFirst: 2, then: `{"tool":"done","summary":"ok"}`})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "test retry")
	d.runTask(sess, task)
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("retry should recover, got %s", tk.Status)
	}
}
