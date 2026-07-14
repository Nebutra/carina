package daemon

import (
	"sync"
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
)

func TestTaskSubmitStoresModelOverride(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	res, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "use a specific model",
		"model":      "openai/gpt-5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := res.(*scheduler.Task)
	if task.Model != "openai/gpt-5" {
		t.Fatalf("model override not stored: %+v", task)
	}
}

func TestTaskSubmitClientSubmissionIDIsConcurrentSafe(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	params := mustJSON(t, map[string]any{
		"session_id":           sess.SessionID,
		"prompt":               "one logical request",
		"client_submission_id": "tui_concurrent_submission",
	})

	const callers = 8
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := d.handleTaskSubmit(params)
			if err != nil {
				errs <- err
				return
			}
			ids <- result.(*scheduler.Task).TaskID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("concurrent retries returned %q and %q", first, id)
		}
	}
	if first == "" || len(d.sched.List()) != 1 {
		t.Fatalf("concurrent idempotency produced first=%q tasks=%d", first, len(d.sched.List()))
	}
}

func TestTaskSubmitClientSubmissionIDIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	ws := t.TempDir()
	d := newDaemonAt(t, stateDir)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	params := map[string]any{
		"session_id":           sess.SessionID,
		"prompt":               "idempotent work",
		"client_submission_id": "tui_test_submission",
	}

	firstAny, err := d.handleTaskSubmit(mustJSON(t, params))
	if err != nil {
		t.Fatal(err)
	}
	secondAny, err := d.handleTaskSubmit(mustJSON(t, params))
	if err != nil {
		t.Fatal(err)
	}
	first, second := firstAny.(*scheduler.Task), secondAny.(*scheduler.Task)
	if first.TaskID != second.TaskID || first.ClientSubmissionID != "tui_test_submission" {
		t.Fatalf("idempotent submit returned %+v then %+v", first, second)
	}
	if got := len(d.sched.List()); got != 1 {
		t.Fatalf("idempotent submit created %d tasks", got)
	}
	loaded := d.runs.load()
	if len(loaded) != 1 || loaded[0].ClientSubmissionFingerprint == "" {
		t.Fatalf("durable submission identity was not restored: %+v", loaded)
	}
	_ = d.Close()
	d = newDaemonAt(t, stateDir)
	defer d.Close()
	restartedAny, err := d.handleTaskSubmit(mustJSON(t, params))
	if err != nil {
		t.Fatal(err)
	}
	if restartedAny.(*scheduler.Task).TaskID != first.TaskID || len(d.sched.List()) != 1 {
		t.Fatalf("restart retry created a duplicate: first=%s retry=%+v list=%+v",
			first.TaskID, restartedAny, d.sched.List())
	}

	params["prompt"] = "different work"
	if _, err := d.handleTaskSubmit(mustJSON(t, params)); err == nil {
		t.Fatal("reusing a client_submission_id for a different prompt was accepted")
	}
	params["prompt"] = "idempotent work"
	params["model"] = "openai/gpt-5"
	if _, err := d.handleTaskSubmit(mustJSON(t, params)); err == nil {
		t.Fatal("reusing a client_submission_id for a different model was accepted")
	}
	for _, invalid := range []string{"", " padded", "含中文", "bad/key"} {
		invalidParams := map[string]any{
			"session_id": sess.SessionID, "prompt": "invalid id", "client_submission_id": invalid,
		}
		if _, err := d.handleTaskSubmit(mustJSON(t, invalidParams)); err == nil {
			t.Fatalf("invalid client_submission_id %q was accepted", invalid)
		}
	}
}
