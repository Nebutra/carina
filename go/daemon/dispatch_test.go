package daemon

import (
	"encoding/json"
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
	"github.com/Nebutra/carina/go/worker"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestWorkDispatchBridge drives the full remote-worker lease protocol end to end
// through the RPC handlers: submit → poll → report, plus empty-poll and
// unknown-worker rejection.
func TestWorkDispatchBridge(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	wk := d.pool.Register("remote-1", worker.Remote)

	// Control plane enqueues work for remote execution.
	subRes, err := d.handleWorkSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "prompt": "build the thing"}))
	if err != nil {
		t.Fatalf("work.submit: %v", err)
	}
	task := subRes.(*scheduler.Task)
	if task.Status != "queued" || task.Mode != "dispatch" {
		t.Fatalf("dispatched task not queued: %+v", task)
	}

	// An unregistered worker cannot poll.
	if _, err := d.handleWorkPoll(mustJSON(t, map[string]any{"worker_id": "ghost"})); err == nil {
		t.Fatal("poll from unregistered worker must be rejected")
	}

	// The registered worker leases the task.
	pollRes, err := d.handleWorkPoll(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "ttl_ms": 5000}))
	if err != nil {
		t.Fatalf("work.poll: %v", err)
	}
	leased, ok := pollRes.(map[string]any)["task"].(*scheduler.Task)
	if !ok || leased.TaskID != task.TaskID || leased.LeaseOwner != wk.WorkerID {
		t.Fatalf("poll did not lease the task: %+v", pollRes)
	}

	// The queue is now empty.
	empty, err := d.handleWorkPoll(mustJSON(t, map[string]any{"worker_id": wk.WorkerID}))
	if err != nil {
		t.Fatalf("empty poll: %v", err)
	}
	if empty.(map[string]any)["empty"] != true {
		t.Fatalf("second poll should be empty, got %+v", empty)
	}

	// The worker renews mid-execution, then reports completion.
	if _, err := d.handleWorkRenew(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "task_id": task.TaskID, "ttl_ms": 5000})); err != nil {
		t.Fatalf("work.renew: %v", err)
	}
	if _, err := d.handleWorkReport(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "task_id": task.TaskID,
		"status": "completed", "summary": "shipped"})); err != nil {
		t.Fatalf("work.report: %v", err)
	}
	got, _ := d.sched.Get(task.TaskID)
	if got.Status != "completed" || got.Summary != "shipped" || got.LeaseOwner != "" {
		t.Fatalf("report did not finalize the task: %+v", got)
	}
}
