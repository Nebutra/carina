package daemon

import (
	"context"
	"sync"
	"testing"
	"time"
)

type cancellationBlockingReasoner struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (r *cancellationBlockingReasoner) Name() string { return "cancellation-blocking" }

func (r *cancellationBlockingReasoner) Think(ctx context.Context, _ string) (string, error) {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	<-r.release
	return "", ctx.Err()
}

// TestCompletionEnvelopeEmitted: a task reaching a terminal state publishes a
// single structured task.completed envelope with the final status and summary.
func TestCompletionEnvelopeEmitted(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	var mu sync.Mutex
	var completions []map[string]any
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "task.completed" {
			mu.Lock()
			completions = append(completions, ev)
			mu.Unlock()
		}
	})

	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"done","summary":"all set"}`,
	}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "do nothing")
	d.runTask(sess, task)

	mu.Lock()
	defer mu.Unlock()
	if len(completions) != 1 {
		t.Fatalf("expected exactly 1 completion envelope, got %d", len(completions))
	}
	env := completions[0]
	if env["status"] != "completed" {
		t.Fatalf("envelope status should be completed, got %v", env["status"])
	}
	if env["summary"] != "all set" {
		t.Fatalf("envelope summary should carry the model summary, got %v", env["summary"])
	}
	if env["task_id"] != task.TaskID {
		t.Fatalf("envelope task_id mismatch: %v", env["task_id"])
	}
	if _, ok := env["duration_ms"]; !ok {
		t.Fatal("envelope should carry duration_ms")
	}
}

func TestTaskCancelWaitsForLocalRunBeforeCompletion(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	reasoner := &cancellationBlockingReasoner{
		started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}),
	}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "wait")

	completions := make(chan map[string]any, 2)
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "task.completed" {
			completions <- ev
		}
	})
	done := make(chan struct{})
	go func() {
		d.runTaskGuarded(sess, task)
		close(done)
	}()
	select {
	case <-reasoner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("reasoner did not start")
	}
	if _, err := d.handleTaskCancel(mustJSON(t, map[string]any{"task_id": task.TaskID})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reasoner.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("task context was not cancelled")
	}
	select {
	case ev := <-completions:
		t.Fatalf("completion emitted before local run exited: %#v", ev)
	default:
	}
	close(reasoner.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled run did not exit")
	}
	select {
	case ev := <-completions:
		if ev["status"] != "cancelled" {
			t.Fatalf("completion status = %v", ev["status"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled completion not emitted")
	}
	if current, ok := d.sched.Get(task.TaskID); !ok || current.Status != "cancelled" {
		t.Fatalf("task status after cancellation = %#v", current)
	}
}

func TestTaskCancelDoesNotWaitForSaturatedRunSemaphore(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.runSem = make(chan struct{}, 1)
	d.runSem <- struct{}{}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "wait for slot")
	done := make(chan struct{})
	go func() {
		d.runTaskGuarded(sess, task)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		d.taskContextMu.Lock()
		_, registered := d.taskCancels[task.TaskID]
		d.taskContextMu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task cancellation context was not registered")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := d.handleTaskCancel(mustJSON(t, map[string]any{"task_id": task.TaskID})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled task remained blocked on run semaphore")
	}
	<-d.runSem
	current, _ := d.sched.Get(task.TaskID)
	if current.Status != "cancelled" {
		t.Fatalf("task status = %s", current.Status)
	}
}
