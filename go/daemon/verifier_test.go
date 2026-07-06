package daemon

import (
	"testing"
)

// TestVerifierDefaultLenient: with no verifier configured, a done is accepted
// immediately (opt-in proof — existing behavior unchanged).
func TestVerifierDefaultLenient(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"x"}`}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "noop")
	d.runTask(sess, task)
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("nil verifier must accept done, got %s", tk.Status)
	}
}

// TestVerifierRejectsThenAccepts: a reject forces another turn; a later pass
// completes the run.
func TestVerifierRejectsThenAccepts(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"done","summary":"first attempt"}`,
		`{"tool":"list"}`,
		`{"tool":"done","summary":"second attempt"}`,
	}})
	d.SetVerifier(&scriptedReasoner{steps: []string{
		`{"verdict":"reject","reason":"not actually done"}`,
		`{"verdict":"pass"}`,
	}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "do work")
	d.runTask(sess, task)
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("reject-then-pass should complete, got %s", tk.Status)
	}
}

// TestVerifierDegradesOnPersistentReject: a verifier that never passes degrades
// the run after the verify budget is exhausted (does not loop forever).
func TestVerifierDegradesOnPersistentReject(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	done := make([]string, 10)
	reject := make([]string, 10)
	for i := range done {
		done[i] = `{"tool":"done","summary":"claim"}`
		reject[i] = `{"verdict":"reject","reason":"still wrong"}`
	}
	d.SetReasoner(&scriptedReasoner{steps: done})
	d.SetVerifier(&scriptedReasoner{steps: reject})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "unsatisfiable")
	d.runTask(sess, task)
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "degraded" {
		t.Fatalf("persistent reject should degrade, got %s", tk.Status)
	}
}

// TestVerifierFailOpenOnError: a verifier transport failure must not block
// completion (fail open).
func TestVerifierFailOpenOnError(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 5 * 1e6 // 5ms
	defer func() { retryBaseDelay = old }()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"x"}`}})
	d.SetVerifier(&flakyReasoner{failFirst: 99}) // never succeeds within retries
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "noop")
	d.runTask(sess, task)
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("verifier error must fail open, got %s", tk.Status)
	}
}

// TestVerifierFailOpenOnMalformedVerdict: an unparseable verdict fails open.
func TestVerifierFailOpenOnMalformedVerdict(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"x"}`}})
	d.SetVerifier(&scriptedReasoner{steps: []string{`not json at all`}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "noop")
	d.runTask(sess, task)
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
		t.Fatalf("malformed verdict must fail open, got %s", tk.Status)
	}
}

func TestParseVerdict(t *testing.T) {
	if v, err := parseVerdict("```json\n{\"verdict\":\"PASS\"}\n```"); err != nil || v.Verdict != "pass" {
		t.Fatalf("fenced pass should parse: %+v err=%v", v, err)
	}
	if v, err := parseVerdict(`{"verdict":"reject","reason":"nope"}`); err != nil || v.Verdict != "reject" || v.Reason != "nope" {
		t.Fatalf("reject should parse: %+v err=%v", v, err)
	}
	if _, err := parseVerdict(`{"verdict":"maybe"}`); err == nil {
		t.Fatal("an invalid verdict value must error")
	}
	if _, err := parseVerdict(`no json`); err == nil {
		t.Fatal("non-json must error")
	}
}
