package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Pure unit tests for MistakeTracker itself (mirrors transcript_test.go's
// LoopGuard coverage: threshold trip, reset-on-success, disabled-when-zero). ---

func TestMistakeTrackerTripsAfterConsecutiveFailures(t *testing.T) {
	m := newMistakeTracker()
	m.MaxConsecutive = 3
	if m.observe(toolFailed("boom", "tool_error")) {
		t.Fatal("1st consecutive failure must not trip")
	}
	if m.observe(toolFailed("boom", "tool_error")) {
		t.Fatal("2nd consecutive failure must not trip")
	}
	if !m.observe(toolFailed("boom", "tool_error")) {
		t.Fatalf("3rd consecutive failure should trip (MaxConsecutive=%d, consecutive=%d)", m.MaxConsecutive, m.consecutive)
	}
}

func TestMistakeTrackerResetsOnCompletedOutcome(t *testing.T) {
	m := newMistakeTracker()
	m.MaxConsecutive = 3
	m.observe(toolFailed("boom", "tool_error"))
	m.observe(toolFailed("boom", "tool_error"))
	if m.observe(toolCompleted("ok")) {
		t.Fatal("a completed outcome must never itself trip the tracker")
	}
	if m.consecutive != 0 {
		t.Fatalf("a completed outcome must reset the consecutive streak, got %d", m.consecutive)
	}
	// Two more failures after the reset must not trip a MaxConsecutive=3
	// tracker — the streak was cleared, not just decremented.
	if m.observe(toolFailed("boom", "tool_error")) {
		t.Fatal("streak should have restarted from zero after the reset")
	}
	if m.observe(toolFailed("boom", "tool_error")) {
		t.Fatal("still only 2 consecutive failures since the reset; must not trip yet")
	}
}

func TestMistakeTrackerCountsMixedFailureKinds(t *testing.T) {
	// denied/timed_out/cancelled all count against the same streak as
	// failed — this is a consecutive-*non-success* breaker, not narrowly
	// scoped to one outcome status.
	m := newMistakeTracker()
	m.MaxConsecutive = 3
	m.observe(toolFailed("boom", "tool_error"))
	m.observe(toolDenied("nope", "policy_denied"))
	if !m.observe(toolTimedOut("slow")) {
		t.Fatal("mixed failure/denied/timed_out outcomes should accumulate on the same streak")
	}
}

func TestMistakeTrackerDisabledWhenZero(t *testing.T) {
	m := newMistakeTracker()
	m.MaxConsecutive = 0
	for i := 0; i < 20; i++ {
		if m.observe(toolFailed("boom", "tool_error")) {
			t.Fatal("MaxConsecutive=0 must disable the circuit breaker, not trip immediately")
		}
	}
}

func TestMistakeTrackerTrippedWithoutNewObservation(t *testing.T) {
	m := newMistakeTracker()
	m.MaxConsecutive = 2
	if m.tripped() {
		t.Fatal("fresh tracker must not report tripped")
	}
	m.observe(toolFailed("boom", "tool_error"))
	m.observe(toolFailed("boom", "tool_error"))
	if !m.tripped() {
		t.Fatal("tripped() should reflect the accumulated streak without a new observe call")
	}
}

func TestMistakeTrackerResetClearsStreak(t *testing.T) {
	m := newMistakeTracker()
	m.MaxConsecutive = 2
	m.observe(toolFailed("boom", "tool_error"))
	m.reset()
	if m.tripped() {
		t.Fatal("reset() must clear an in-progress streak")
	}
	if m.observe(toolFailed("boom", "tool_error")) {
		t.Fatal("post-reset, a single failure must not immediately trip")
	}
}

// --- Wired-in integration test: the main runLoop must actually degrade a
// task that keeps hitting a failing tool call, independent of LoopGuard
// (each read targets a different missing path, so no signature repeats and
// LoopGuard's hard-repeat threshold never fires). ---

func TestMistakeTrackerDegradesOnConsecutiveToolFailures(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	// Every step reads a distinct nonexistent file (distinct action
	// signatures -> LoopGuard never trips), but every read fails with
	// io_error -> MistakeTracker's consecutive-failure streak should trip
	// well before maxAgentTurns (14) or LoopGuard's MaxHardRepeat (6).
	steps := []string{
		`{"tool":"read","path":"missing-1.txt"}`,
		`{"tool":"read","path":"missing-2.txt"}`,
		`{"tool":"read","path":"missing-3.txt"}`,
		`{"tool":"read","path":"missing-4.txt"}`,
	}
	d.SetReasoner(&scriptedReasoner{steps: steps})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "read files that don't exist")
	d.runTask(sess, task)

	tk, ok := d.sched.Get(task.TaskID)
	if !ok || tk.Status != "degraded" {
		t.Fatalf("consecutive tool failures should degrade the task, got %+v (ok=%v)", tk, ok)
	}
	if !strings.Contains(tk.Summary, "mistake tracker") {
		t.Fatalf("degrade reason should mention the mistake tracker, got %q", tk.Summary)
	}
}

// TestMistakeTrackerDoesNotTripOnAlternatingSuccessFailure proves the streak
// is consecutive, not cumulative: alternating a real (successful) read with
// a failing one must never trip a MaxConsecutive=3 breaker, even across many
// turns, because every success resets the count back to zero.
func TestMistakeTrackerDoesNotTripOnAlternatingSuccessFailure(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "present.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	steps := []string{
		`{"tool":"read","path":"present.txt"}`,
		`{"tool":"read","path":"missing-a.txt"}`,
		`{"tool":"read","path":"present.txt"}`,
		`{"tool":"read","path":"missing-b.txt"}`,
		`{"tool":"read","path":"present.txt"}`,
		`{"tool":"read","path":"missing-c.txt"}`,
		`{"tool":"done","summary":"ok"}`,
	}
	d.SetReasoner(&scriptedReasoner{steps: steps})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "alternate reads")
	d.runTask(sess, task)

	tk, ok := d.sched.Get(task.TaskID)
	if !ok || tk.Status != "completed" {
		t.Fatalf("alternating success/failure must not trip the consecutive-failure breaker, got %+v (ok=%v)", tk, ok)
	}
}
