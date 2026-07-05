package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// panicReasoner blows up on every call — used to prove a panic in the agent
// loop is contained to its own run and never crashes the daemon.
type panicReasoner struct{}

func (panicReasoner) Name() string                                  { return "panic" }
func (panicReasoner) Think(context.Context, string) (string, error) { panic("boom") }

// newDaemonAt builds a daemon rooted at a caller-chosen state dir, so a test can
// close it and reopen a fresh daemon on the SAME state dir to simulate a restart.
func newDaemonAt(t *testing.T, stateDir string) *Daemon {
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
	d, err := New(Options{StateDir: stateDir, KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRunRegistrySurvivesRestart: a completed background run is still listable
// and its result still queryable after the daemon is torn down and a fresh one
// opens on the same state directory.
func TestRunRegistrySurvivesRestart(t *testing.T) {
	stateDir := t.TempDir()
	ws := t.TempDir()

	d1 := newDaemonAt(t, stateDir)
	d1.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"all done"}`}})
	sess, _ := d1.store.CreateSession(ws, "safe-edit")
	d1.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d1.sched.Submit(sess.SessionID, sess.WorkspaceID, "noop")
	d1.runTaskGuarded(sess, task)
	if tk, _ := d1.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("run should complete, got %s", tk.Status)
	}
	d1.Close()

	// Restart on the same state dir: the run record must come back.
	d2 := newDaemonAt(t, stateDir)
	defer d2.Close()
	tk, ok := d2.sched.Get(task.TaskID)
	if !ok {
		t.Fatal("run not recovered after restart (registry not durable)")
	}
	if tk.Status != "completed" || tk.Summary != "all done" {
		t.Fatalf("recovered run wrong: status=%s summary=%q", tk.Status, tk.Summary)
	}
	// It must also show up in the registry listing.
	found := false
	for _, r := range d2.sched.List() {
		if r.TaskID == task.TaskID {
			found = true
		}
	}
	if !found {
		t.Fatal("recovered run missing from task list")
	}
}

// TestBackgroundRunPanicIsolation: a run whose reasoner panics is marked failed,
// the daemon stays alive, and subsequent runs still complete.
func TestBackgroundRunPanicIsolation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	d.SetReasoner(panicReasoner{})
	bad := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "boom")
	d.runTaskGuarded(sess, bad) // must NOT crash the test process
	if tk, _ := d.sched.Get(bad.TaskID); tk.Status != "failed" {
		t.Fatalf("panicking run should be marked failed, got %s", tk.Status)
	}

	// The daemon must still work after a recovered panic.
	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"ok"}`}})
	good := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "ok")
	d.runTaskGuarded(sess, good)
	if tk, _ := d.sched.Get(good.TaskID); tk.Status != "completed" {
		t.Fatalf("daemon should still complete tasks after a panic, got %s", tk.Status)
	}
}

// TestBackgroundRunResumesAfterRestart: a run interrupted mid-flight (persisted
// as "running" with a transcript checkpoint) is resumed by a fresh daemon and
// driven to completion — the core "durable background agent" behavior.
func TestBackgroundRunResumesAfterRestart(t *testing.T) {
	stateDir := t.TempDir()
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hi\n"), 0o600)

	// d1: create a session + task and simulate a mid-run crash — status "running"
	// with a one-turn transcript checkpoint, then Close before it finished.
	d1 := newDaemonAt(t, stateDir)
	sess, _ := d1.store.CreateSession(ws, "safe-edit")
	d1.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d1.sched.Submit(sess.SessionID, sess.WorkspaceID, "resume me")
	d1.sched.SetStatus(task.TaskID, "running")
	tr := newTranscript(task.UserPrompt)
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.txt", Obs: Observation{Content: "hi"}})
	d1.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: 1, Transcript: tr})
	d1.persistRun(task.TaskID)
	d1.Close()

	// d2: restart on the same state dir. New()'s resumeRuns() no-ops (offline =>
	// nil reasoner) and leaves the run "running". Inject a reasoner, then resume.
	d2 := newDaemonAt(t, stateDir)
	defer d2.Close()
	if tk, ok := d2.sched.Get(task.TaskID); !ok || tk.Status != "running" {
		t.Fatalf("interrupted run should be preserved as running for resume, got %+v", tk)
	}
	d2.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"resumed and finished"}`}})
	d2.resumeRuns()

	// Resume is async; poll for terminal state.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if tk, _ := d2.sched.Get(task.TaskID); tk.Status == "completed" {
			if tk.Summary != "resumed and finished" {
				t.Fatalf("resumed run wrong summary: %q", tk.Summary)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tk, _ := d2.sched.Get(task.TaskID)
	t.Fatalf("resumed run did not complete in time, status=%s", tk.Status)
}
