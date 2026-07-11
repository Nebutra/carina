package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	"github.com/Nebutra/carina/go/worker"
)

// simulateRemoteWorker plays the role of a real apps/carina-worker process
// against the SAME work.poll/work.report RPC handlers that binary calls —
// polling (with a short retry loop, since the dispatch queue may not have
// the task yet the instant this goroutine starts) until it leases exactly
// one task, then reporting status/summary for it. Returns the leased task's
// ID, or fails the test if nothing was ever leased within the deadline.
// simulateRemoteWorker is always invoked via `go simulateRemoteWorker(...)`,
// so it must never call t.Fatal/t.Fatalf/t.FailNow (those require running on
// the test's own goroutine) — only the goroutine-safe t.Errorf, matching
// `go vet`'s tests analyzer.
func simulateRemoteWorker(t *testing.T, d *Daemon, workerID, credential, status, summary string) string {
	t.Helper()
	poll := func(v any) (json.RawMessage, error) { return json.Marshal(v) }
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		params, _ := poll(map[string]any{"worker_id": workerID, "worker_credential": credential, "ttl_ms": 5000})
		res, err := d.handleWorkPoll(params)
		if err != nil {
			t.Errorf("work.poll: %v", err)
			return ""
		}
		m := res.(map[string]any)
		if m["empty"] == true {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		leased := m["task"].(*scheduler.Task)
		reportParams, _ := poll(map[string]any{
			"worker_id": workerID, "worker_credential": credential,
			"task_id": leased.TaskID, "lease_generation": leased.LeaseGeneration,
			"status": status, "summary": summary,
		})
		if _, err := d.handleWorkReport(reportParams); err != nil {
			t.Errorf("work.report: %v", err)
			return ""
		}
		return leased.TaskID
	}
	t.Errorf("simulateRemoteWorker: no task was ever leased within the deadline")
	return ""
}

func TestWorkflowStreamingRemoteStepDispatchesLeasesAndCompletes(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	wk, credential, err := d.pool.RegisterAuthenticated("remote-1", worker.Remote)
	if err != nil {
		t.Fatal(err)
	}

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	go simulateRemoteWorker(t, d, wk.WorkerID, credential, "completed", "did the remote thing")

	spec := &WorkflowSpec{Name: "remote", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "offload", Agent: "irrelevant-for-remote", Task: "REMOTE_STEP", Remote: true},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-remote")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if out["offload"] != "did the remote thing" {
		t.Fatalf("expected the remote worker's report summary as the step output, got %q", out["offload"])
	}
}

func TestWorkflowStreamingRemoteStepFailureIsolatesLikeLocalSteps(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	wk, credential, err := d.pool.RegisterAuthenticated("remote-1", worker.Remote)
	if err != nil {
		t.Fatal(err)
	}

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	go simulateRemoteWorker(t, d, wk.WorkerID, credential, "failed", "the remote executor blew up")

	spec := &WorkflowSpec{Name: "remote-fail", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "offload", Agent: "worker", Task: "REMOTE_FAIL_STEP", Remote: true},
		{ID: "dependent", Agent: "worker", Task: "should be skipped", Needs: []string{"offload"}},
		{ID: "independent", Agent: "worker", Task: "unrelated work"},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-remote-fail")
	if err != nil {
		t.Fatalf("a remote step failure must isolate, not abort the whole run (default on_failure): %v", err)
	}
	if _, ok := out["dependent"]; ok {
		t.Fatal("dependent on a failed remote step should have been skipped")
	}
	if _, ok := out["independent"]; !ok {
		t.Fatal("a step with no dependency on the failed remote step should still have run")
	}
}

func TestWorkflowStreamingRemoteStepFailFastAbortsWholeRun(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	wk, credential, err := d.pool.RegisterAuthenticated("remote-1", worker.Remote)
	if err != nil {
		t.Fatal(err)
	}

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	go simulateRemoteWorker(t, d, wk.WorkerID, credential, "failed", "critical remote failure")

	spec := &WorkflowSpec{Name: "remote-failfast", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "offload", Agent: "irrelevant-for-remote", Task: "REMOTE_CRITICAL_STEP", Remote: true, FailFast: true},
	}}
	_, err = d.runWorkflowStreaming(parent, parentTask, spec, "", "run-remote-failfast")
	if err == nil {
		t.Fatal("expected a fail-fast remote step failure to abort the run with an error")
	}
	if !strings.Contains(err.Error(), "critical remote failure") {
		t.Fatalf("expected the remote failure reason to propagate into the run error, got: %v", err)
	}
}

func TestWorkflowStreamingRemoteStepCancelledStopsWaitingAndCancelsDispatchTask(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)

	res := d.runStreamingStepRemote(ctx, parent, parentTask, &WorkflowSpec{Name: "remote-cancel"}, "run-remote-cancel",
		WorkflowStep{ID: "offload", Task: "NEVER_LEASED_STEP", Remote: true}, "NEVER_LEASED_STEP")
	if res.kind != stepSkipped {
		t.Fatalf("expected a cancelled wait to resolve as skipped, got kind=%v errMsg=%q", res.kind, res.errMsg)
	}
}

func TestWorkflowStreamingRemoteStepAffinityRestrictsRequiredWorkerCapabilities(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	// A worker that does NOT advertise the requested pool must never be
	// offered the task; one that does must be able to lease it. Rather than
	// extending the real worker-registration RPC surface (a separate,
	// deliberately out-of-scope change — see workflow_remote.go's Affinity
	// doc comment), this test manipulates the registered Worker's
	// Capabilities directly, which is exactly the field LeaseMatching's
	// Supports() check reads.
	plain, plainCred, err := d.pool.RegisterAuthenticated("plain", worker.Remote)
	if err != nil {
		t.Fatal(err)
	}
	tagged, taggedCred, err := d.pool.RegisterAuthenticated("gpu-worker", worker.Remote)
	if err != nil {
		t.Fatal(err)
	}
	tagged.Capabilities = append(tagged.Capabilities, "worker_pool:gpu-heavy")

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	// The plain worker polls first and repeatedly — it must NEVER see this
	// task, only ever get "empty", for as long as the affinity-tagged step
	// hasn't yet been leased by the correctly-tagged worker.
	stopPlainPolling := make(chan struct{})
	plainSawTask := make(chan string, 1)
	go func() {
		for {
			select {
			case <-stopPlainPolling:
				return
			default:
			}
			pollParams, _ := json.Marshal(map[string]any{
				"worker_id": plain.WorkerID, "worker_credential": plainCred, "ttl_ms": 5000,
			})
			res, err := d.handleWorkPoll(pollParams)
			if err != nil {
				return
			}
			m := res.(map[string]any)
			if m["empty"] != true {
				leased := m["task"].(*scheduler.Task)
				select {
				case plainSawTask <- leased.TaskID:
				default:
				}
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	defer close(stopPlainPolling)

	go simulateRemoteWorker(t, d, tagged.WorkerID, taggedCred, "completed", "handled by the gpu pool")

	spec := &WorkflowSpec{Name: "remote-affinity", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "offload", Agent: "irrelevant-for-remote", Task: "GPU_STEP", Remote: true, Affinity: map[string]string{"worker_pool": "gpu-heavy"}},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-remote-affinity")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if out["offload"] != "handled by the gpu pool" {
		t.Fatalf("expected the affinity-tagged worker's report, got %q", out["offload"])
	}
	select {
	case leakedTaskID := <-plainSawTask:
		t.Fatalf("a worker without the gpu-heavy pool capability must never lease an affinity-tagged task, but leased %q", leakedTaskID)
	default:
	}
}
