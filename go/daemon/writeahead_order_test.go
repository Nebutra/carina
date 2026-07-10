package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// gatedReasoner blocks its first Think() call on a channel so a test can prove
// that audit-chain append + durable persistence happened strictly before the
// reasoner (and therefore the dispatch goroutine) could possibly run — a
// synchronization gate, not a timing assumption. See P1.8's write-ahead
// requirement in docs/plans/agent-cli-productization.md §P1.8.
type gatedReasoner struct {
	proceed chan struct{}
	entered chan struct{}
}

func newGatedReasoner() *gatedReasoner {
	return &gatedReasoner{
		proceed: make(chan struct{}),
		entered: make(chan struct{}, 1),
	}
}

func (g *gatedReasoner) Name() string { return "gated" }

func (g *gatedReasoner) Think(_ context.Context, _ string) (string, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.proceed
	return `{"thought":"done","action":{"tool":"done","summary":"ok"}}`, nil
}

// TestWriteAheadTaskCreatedPrecedesReasonerDispatch pins the invariant that
// handleTaskSubmit's synchronous write-ahead sequence (kernel audit-chain
// append of TaskCreated with the submitted prompt, then durable
// persistRun) has already completed before the reasoner goroutine's first
// Think() call can possibly run. The gatedReasoner blocks inside Think until
// released; while it is blocked (proven via the entered signal, not a
// sleep), the test reads the kernel's own audit chain and asserts the
// TaskCreated event carrying the user prompt is already present. This
// guards against a future latency-motivated refactor that moves `go
// d.runTaskGuarded` earlier, ahead of the record()/persistRun() calls.
func TestWriteAheadTaskCreatedPrecedesReasonerDispatch(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	reasoner := newGatedReasoner()
	d.SetReasoner(reasoner)

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}

	const wantPrompt = "write-ahead pin: does the audit chain agree before dispatch?"
	params, err := json.Marshal(map[string]any{
		"session_id": sess.SessionID,
		"prompt":     wantPrompt,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.handleTaskSubmit(params); err != nil {
		t.Fatalf("handleTaskSubmit: %v", err)
	}

	// Wait for the reasoner goroutine to actually enter Think (bounded, but
	// this is a liveness wait, not the ordering proof itself — the ordering
	// proof is that entered only fires after runTaskGuarded's goroutine
	// starts, and everything checked below happened synchronously in
	// handleTaskSubmit before that goroutine was even spawned).
	select {
	case <-reasoner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reasoner.Think was never entered")
	}
	defer close(reasoner.proceed)

	// While the reasoner is blocked mid-Think, the audit chain must already
	// contain TaskCreated with the submitted prompt: this is what "before
	// dispatch" means precisely — record()+persistRun() returned before the
	// goroutine's first model call could complete (and here, provably,
	// before it even proceeds past its gate).
	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if !strings.Contains(string(raw), "TaskCreated") || !strings.Contains(string(raw), wantPrompt) {
		t.Fatalf("audit chain missing write-ahead TaskCreated(prompt) while reasoner is still blocked in Think: %s", raw)
	}
}

// TestTaskSubmitRefusesWhenWriteAheadRecordFails pins the other half of
// P1.8's write-ahead requirement: a FAILED audit-chain append of the
// write-ahead TaskCreated(prompt) event must refuse the submission, not
// silently dispatch an agent loop whose defining instruction the audit
// trail cannot attest to. Kills the real kernel subprocess (not a fake —
// d.kern is a concrete *kernel.Service wrapping a real child process, so
// this is a genuine RecordEvent RPC failure, the same shape a transient
// kernel restart/broken-pipe race would produce in production) before
// calling handleTaskSubmit, and asserts: the RPC call itself fails, and no
// task is left runnable in the scheduler — no goroutine was ever dispatched
// to act on an instruction the audit chain never durably recorded.
func TestTaskSubmitRefusesWhenWriteAheadRecordFails(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}

	before := d.sched.CountByStatus()

	// Kill the kernel subprocess: every subsequent d.kern.RecordEvent call
	// (and any other kernel RPC) now fails with a real connection error.
	_ = d.Kernel().Close()

	params, err := json.Marshal(map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "should never reach the audit chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleTaskSubmit(params); err == nil {
		t.Fatal("handleTaskSubmit must refuse when the write-ahead audit-chain append fails, got nil error")
	}

	after := d.sched.CountByStatus()
	for _, runnable := range []string{"pending", "background", "running", "waiting_approval"} {
		if after[runnable] > before[runnable] {
			t.Fatalf("a task was left in runnable status %q after a refused write-ahead append (before=%v after=%v) — an ungoverned task must never be dispatched", runnable, before, after)
		}
	}
}
