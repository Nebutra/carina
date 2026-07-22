package daemon

import (
	"strings"
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
	if task.RequestedModel != "openai/gpt-5" || task.EffectiveModel != "openai/gpt-5" || task.Mode != "background" {
		t.Fatalf("model state/envelope not stored: %+v", task)
	}
}

func TestTaskSubmitValidatesProviderModelAndPersistsModelState(t *testing.T) {
	stateDir := t.TempDir()
	d := newDaemonAt(t, stateDir)
	sess, _ := d.store.CreateSession(t.TempDir(), "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, sess.WorkspaceRoot, "safe-edit", nil)
	if _, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "prompt": "bad model", "model": "unknown-provider/model",
	})); err == nil {
		t.Fatal("unknown provider model was accepted")
	}
	if _, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "prompt": "bad model", "model": "openai/gpt-5\nother",
	})); err == nil {
		t.Fatal("model with control characters was accepted")
	}
	result, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "prompt": "valid", "model": "openai/gpt-5", "mode": "background",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := result.(*scheduler.Task)
	_ = d.Close()
	expected, ok := d.sched.Get(task.TaskID)
	if !ok || expected.EffectiveModel == "" {
		t.Fatalf("effective model was not resolved before persistence: %+v", expected)
	}
	d = newDaemonAt(t, stateDir)
	defer d.Close()
	reloaded, ok := d.sched.Get(task.TaskID)
	if !ok || reloaded.RequestedModel != "openai/gpt-5" || reloaded.EffectiveModel != expected.EffectiveModel || reloaded.Mode != "background" {
		t.Fatalf("durable model state changed: %+v ok=%v", reloaded, ok)
	}
}

func TestTaskSubmitRejectsDisabledProvider(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.disabledProviders = disabledProviderSet([]string{"OPENAI"})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	if _, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "do not route this",
		"model":      "openai/gpt-5",
	})); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled provider submission error = %v", err)
	}
}

func TestEffectiveModelName(t *testing.T) {
	if got := effectiveModelName(ModelUsage{Provider: "openai", Model: "gpt-5"}); got != "openai/gpt-5" {
		t.Fatalf("effective model = %q", got)
	}
	if got := effectiveModelName(ModelUsage{Provider: "openrouter", Model: "openrouter/anthropic/claude"}); got != "openrouter/anthropic/claude" {
		t.Fatalf("qualified effective model = %q", got)
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
