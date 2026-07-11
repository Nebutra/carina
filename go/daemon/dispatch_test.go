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
	wk, credential, err := d.pool.RegisterAuthenticated("remote-1", worker.Remote)
	if err != nil {
		t.Fatal(err)
	}

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

	// High worker pressure is an explicit, TTL-bound advisory signal: poll
	// returns empty with a directive and does not lease the queued task.
	pressureRes, err := d.handleBackpressureReport(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "worker_credential": credential, "mem_usage_permille": 960, "queue_depth": 20, "seq": 1}))
	if err != nil {
		t.Fatalf("backpressure.report high: %v", err)
	}
	if !pressureRes.(map[string]any)["accepted"].(bool) {
		t.Fatalf("first pressure report should be accepted: %+v", pressureRes)
	}
	throttled, err := d.handleWorkPoll(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "worker_credential": credential, "ttl_ms": 5000}))
	if err != nil {
		t.Fatalf("throttled work.poll: %v", err)
	}
	throttledMap := throttled.(map[string]any)
	if throttledMap["empty"] != true {
		t.Fatalf("high pressure should suppress leasing: %+v", throttledMap)
	}
	if directive, ok := throttledMap["backpressure"].(ThrottleDirective); !ok || directive.Level != "pause" || directive.MaxInflight != 0 {
		t.Fatalf("missing pause directive: %+v", throttledMap["backpressure"])
	}
	if queued, _ := d.sched.Get(task.TaskID); queued.Status != "queued" {
		t.Fatalf("throttled poll must not mutate queued task: %+v", queued)
	}
	pressureRes, err = d.handleBackpressureReport(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "worker_credential": credential, "mem_usage_permille": 100, "queue_depth": 0, "seq": 2}))
	if err != nil {
		t.Fatalf("backpressure.report recovery: %v", err)
	}
	if directive := pressureRes.(map[string]any)["directive"].(ThrottleDirective); directive.Level != "none" || directive.MaxInflight != 1 {
		t.Fatalf("recovered pressure should clear throttling: %+v", directive)
	}
	staleRes, err := d.handleBackpressureReport(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "worker_credential": credential, "mem_usage_permille": 990, "seq": 1}))
	if err != nil {
		t.Fatalf("backpressure.report stale: %v", err)
	}
	if staleRes.(map[string]any)["accepted"].(bool) {
		t.Fatalf("stale pressure report should be rejected: %+v", staleRes)
	}

	// The registered worker leases the task.
	pollRes, err := d.handleWorkPoll(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "worker_credential": credential, "ttl_ms": 5000}))
	if err != nil {
		t.Fatalf("work.poll: %v", err)
	}
	leased, ok := pollRes.(map[string]any)["task"].(*scheduler.Task)
	if !ok || leased.TaskID != task.TaskID || leased.LeaseOwner != wk.WorkerID || leased.LeaseGeneration <= 0 {
		t.Fatalf("poll did not lease the task: %+v", pollRes)
	}

	// The queue is now empty.
	empty, err := d.handleWorkPoll(mustJSON(t, map[string]any{"worker_id": wk.WorkerID, "worker_credential": credential}))
	if err != nil {
		t.Fatalf("empty poll: %v", err)
	}
	if empty.(map[string]any)["empty"] != true {
		t.Fatalf("second poll should be empty, got %+v", empty)
	}

	// The worker renews mid-execution, then reports completion.
	if _, err := d.handleWorkRenew(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "worker_credential": credential, "task_id": task.TaskID,
		"lease_generation": leased.LeaseGeneration, "ttl_ms": 5000})); err != nil {
		t.Fatalf("work.renew: %v", err)
	}
	if _, err := d.handleWorkReport(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "worker_credential": credential, "task_id": task.TaskID,
		"lease_generation": leased.LeaseGeneration, "status": "completed", "summary": "shipped"})); err != nil {
		t.Fatalf("work.report: %v", err)
	}
	got, _ := d.sched.Get(task.TaskID)
	if got.Status != "completed" || got.Summary != "shipped" || got.LeaseOwner != "" {
		t.Fatalf("report did not finalize the task: %+v", got)
	}
}

func TestBackpressureStatusIncludesSchedulerContext(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	wk, credential, err := d.pool.RegisterAuthenticated("remote-1", worker.Remote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleWorkSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "prompt": "queued"})); err != nil {
		t.Fatalf("work.submit: %v", err)
	}
	if _, err := d.handleBackpressureReport(mustJSON(t, map[string]any{
		"worker_id": wk.WorkerID, "worker_credential": credential, "queue_depth": 9, "inflight": 1, "seq": 1})); err != nil {
		t.Fatalf("backpressure.report: %v", err)
	}
	status, err := d.handleBackpressureStatus(nil)
	if err != nil {
		t.Fatalf("backpressure.status: %v", err)
	}
	m := status.(map[string]any)
	if m["ttl_seconds"].(int) <= 0 {
		t.Fatalf("status should include ttl_seconds: %+v", m)
	}
	if got := len(m["reports"].([]PressureReport)); got != 1 {
		t.Fatalf("expected one pressure report, got %d: %+v", got, m)
	}
	directives := m["directives"].([]ThrottleDirective)
	if len(directives) != 1 || directives[0].Level != "warn" {
		t.Fatalf("expected warn directive: %+v", directives)
	}
	scheduler := m["scheduler"].(map[string]any)
	if scheduler["dispatch_depth"].(int) != 1 {
		t.Fatalf("scheduler context should include dispatch depth: %+v", scheduler)
	}
}
