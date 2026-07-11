package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/workflowui"
)

func writeWorkflowSpecFile(t *testing.T, ws string, raw string) {
	t.Helper()
	dir := filepath.Join(ws, ".carina", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForTerminalRun(t *testing.T, d *Daemon, runID string) workflowui.Detail {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := d.workflowRuns.Detail(runID)
		if err != nil {
			t.Fatalf("workflowRuns.Detail: %v", err)
		}
		switch detail.Run.Status {
		case workflowui.Completed, workflowui.Failed, workflowui.Stopped, workflowui.Interrupted:
			return detail
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workflow run never reached a terminal status")
	return workflowui.Detail{}
}

// TestHandleWorkflowRunHonorsStreamingExecutionMode is the regression test
// for the bug found while answering "真能消费吗": the workflow.run RPC —
// the actual production entry point an external client/CLI uses, as
// opposed to the model-driven "workflow" tool call — used to unconditionally
// call the BSP runWorkflow, silently ignoring "execution_mode":"streaming".
// This proves a streaming-mode step actually runs through
// runWorkflowStreaming when started via the RPC path, and that its live
// per-step status lands in workflowui (what workflow.detail actually
// returns to a real caller).
func TestHandleWorkflowRunHonorsStreamingExecutionMode(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})
	writeWorkflowSpecFile(t, ws, `{
		"name": "rpc-streaming",
		"execution_mode": "streaming",
		"steps": [
			{"id": "a", "agent": "worker", "task": "A"},
			{"id": "b", "agent": "worker", "task": "B", "needs": ["a"]}
		]
	}`)

	sess, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil)

	res, err := d.handleWorkflowRun(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "workflow": "rpc-streaming", "input": "",
	}))
	if err != nil {
		t.Fatalf("workflow.run: %v", err)
	}
	run := res.(workflowui.Run)

	detail := waitForTerminalRun(t, d, run.ID)
	if detail.Run.Status != workflowui.Completed {
		t.Fatalf("expected the run to complete, got status=%s run=%+v", detail.Run.Status, detail.Run)
	}
	if detail.Completed != 2 || detail.Total != 2 || detail.Progress != 1.0 {
		t.Fatalf("expected 2/2 completed steps with progress=1.0, got %+v", detail)
	}
}

// TestHandleWorkflowRunStreamingIsolatedFailureReflectsAsSkippedNotStuck
// proves the second half of the same fix: a step resolved via markSkipped
// (never actually dispatched — here, a false conditional edge) now gets a
// real terminal workflowui.Step status instead of sitting at the Create()-
// time default of Queued forever, and workflow.detail's Progress reaches
// 1.0 once the run is genuinely done rather than looking permanently stuck.
func TestHandleWorkflowRunStreamingSkippedStepReflectsInDetail(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})
	writeWorkflowSpecFile(t, ws, `{
		"name": "rpc-skip",
		"execution_mode": "streaming",
		"steps": [
			{"id": "gate", "agent": "worker", "task": "GATE"},
			{"id": "conditional", "agent": "worker", "task": "SHOULD_NOT_RUN", "needs": ["gate"],
			 "when": {"==": [{"var":"gate.nonexistent_field"}, "yes"]}}
		]
	}`)

	sess, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil)

	res, err := d.handleWorkflowRun(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "workflow": "rpc-skip", "input": "",
	}))
	if err != nil {
		t.Fatalf("workflow.run: %v", err)
	}
	run := res.(workflowui.Run)

	detail := waitForTerminalRun(t, d, run.ID)
	if detail.Run.Status != workflowui.Completed {
		t.Fatalf("an isolated skip must not fail the whole run, got status=%s", detail.Run.Status)
	}
	if detail.Completed != 1 || detail.Skipped != 1 || detail.Progress != 1.0 {
		t.Fatalf("expected 1 completed + 1 skipped with progress=1.0 (not stuck at 0.5 forever), got %+v", detail)
	}
	for _, st := range detail.Run.Steps {
		if st.ID == "conditional" && st.Status != workflowui.Skipped {
			t.Fatalf("expected the conditional step's workflowui status to be %q, got %q", workflowui.Skipped, st.Status)
		}
	}
}

// TestHandleWorkflowRunStreamingFailedStepReflectsInDetailNotStuckRunning is
// the regression test for a bug found via a live end-to-end smoke test
// (carina workflow run against a real daemon): a step whose SPAWN itself
// fails (e.g. an unknown agent name) used to leave its workflowui.Step
// permanently stuck at "running" — set right before spawn, never updated
// away from it on the failure path — even though the coordinator's own
// graph state correctly resolved it as failed and propagated skips to its
// dependents. `carina workflow status`/workflow.detail showed a "completed"
// run with one step frozen at "running" forever.
func TestHandleWorkflowRunStreamingFailedStepReflectsInDetailNotStuckRunning(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})
	writeWorkflowSpecFile(t, ws, `{
		"name": "rpc-fail",
		"execution_mode": "streaming",
		"steps": [
			{"id": "bad", "agent": "does-not-exist", "task": "X"},
			{"id": "dependent", "agent": "worker", "task": "Y", "needs": ["bad"]}
		]
	}`)

	sess, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil)

	res, err := d.handleWorkflowRun(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "workflow": "rpc-fail", "input": "",
	}))
	if err != nil {
		t.Fatalf("workflow.run: %v", err)
	}
	run := res.(workflowui.Run)

	detail := waitForTerminalRun(t, d, run.ID)
	if detail.Failed != 1 || detail.Skipped != 1 || detail.Progress != 1.0 {
		t.Fatalf("expected 1 failed + 1 skipped with progress=1.0, got %+v", detail)
	}
	for _, st := range detail.Run.Steps {
		switch st.ID {
		case "bad":
			if st.Status != workflowui.Failed {
				t.Fatalf(`expected "bad"'s workflowui status to be %q (not stuck at %q), got %q`, workflowui.Failed, workflowui.Running, st.Status)
			}
			if st.FinishedAt == nil {
				t.Fatal(`expected "bad" to have a FinishedAt once resolved`)
			}
		case "dependent":
			if st.Status != workflowui.Skipped {
				t.Fatalf("expected dependent's workflowui status to be %q, got %q", workflowui.Skipped, st.Status)
			}
		}
	}
}

// TestHandleWorkerRegisterValidatesPoolTags is the server-side authoritative
// validation for --pool (apps/carina-worker's own check is a client-side
// fast-fail mirror, not the trust boundary).
func TestHandleWorkerRegisterValidatesPoolTags(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()

	if _, err := d.handleWorkerRegister(mustJSON(t, map[string]any{
		"name": "w1", "kind": "remote", "pools": []string{"gpu-heavy", "eu-west"},
	})); err != nil {
		t.Fatalf("valid pool tags should register cleanly: %v", err)
	}
	if _, err := d.handleWorkerRegister(mustJSON(t, map[string]any{
		"name": "w2", "kind": "remote", "pools": []string{"GPU Heavy!"},
	})); err == nil {
		t.Fatal("expected an invalid pool tag to be rejected")
	}
	tooMany := make([]string, maxWorkerRegisterPools+1)
	for i := range tooMany {
		tooMany[i] = "p"
	}
	if _, err := d.handleWorkerRegister(mustJSON(t, map[string]any{
		"name": "w3", "kind": "remote", "pools": tooMany,
	})); err == nil {
		t.Fatalf("expected more than %d pool tags to be rejected", maxWorkerRegisterPools)
	}
}

// TestWorkflowStreamingRemoteAffinityRoutesToARealRegisteredPoolWorker is
// the end-to-end complement to workflow_remote_test.go's
// TestWorkflowStreamingRemoteStepAffinityRestrictsRequiredWorkerCapabilities
// (which proved the MATCHING mechanism by mutating Worker.Capabilities
// directly, in-process — not something a real operator can do). This
// version registers the worker through the REAL worker.register RPC with
// --pool-equivalent params, the exact path apps/carina-worker now uses,
// proving an operator-declared pool tag is what actually makes affinity
// routing work end to end, not just the underlying scheduler primitive.
func TestWorkflowStreamingRemoteAffinityRoutesToARealRegisteredPoolWorker(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	regRes, err := d.handleWorkerRegister(mustJSON(t, map[string]any{
		"name": "gpu-worker", "kind": "remote", "pools": []string{"gpu-heavy"},
	}))
	if err != nil {
		t.Fatalf("worker.register: %v", err)
	}
	reg := regRes.(map[string]any)
	workerID, credential := reg["worker_id"].(string), reg["worker_credential"].(string)

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	go simulateRemoteWorker(t, d, workerID, credential, "completed", "handled by a really-registered gpu worker")

	spec := &WorkflowSpec{Name: "remote-real-affinity", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "offload", Agent: "irrelevant-for-remote", Task: "GPU_STEP", Remote: true, Affinity: map[string]string{"worker_pool": "gpu-heavy"}},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-real-affinity")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if out["offload"] != "handled by a really-registered gpu worker" {
		t.Fatalf("expected the real registered pool worker's report, got %q", out["offload"])
	}
}
