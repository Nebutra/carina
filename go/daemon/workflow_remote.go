package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// remoteDispatchPollInterval is how often runStreamingStepRemote checks
// scheduler state while waiting for an external worker (apps/carina-worker,
// or any process speaking the work.poll/work.renew/work.report RPC
// contract in go/daemon/dispatch.go) to lease and report a dispatched step.
// Short enough that an in-process simulated worker in tests doesn't add
// noticeable latency, long enough not to busy-loop the scheduler's mutex.
const remoteDispatchPollInterval = 20 * time.Millisecond

// gateRemoteDispatch evaluates the RemoteDispatch capability before a
// workflow step's task is placed on the cross-process dispatch queue.
// Deliberately distinct from SubagentSpawn (Agent Swarm design §7.3):
// trusting an external worker process — authenticated only by a bearer
// credential, potentially on a different machine, outside this daemon's own
// attenuate() chain — to execute governed side effects is a
// different-magnitude trust decision than a same-process, capability-
// attenuated child session, so it gets its own gate and its own (stronger,
// RequiresApproval) default verdict rather than reusing a weaker one.
func (d *Daemon) gateRemoteDispatch(parent *sessionstore.Session, parentTask *scheduler.Task, resource string) (bool, *kernel.Decision) {
	dec, err := d.kern.Request(parent.SessionID, "RemoteDispatch", resource, parentTask.TaskID)
	if err != nil {
		return false, &kernel.Decision{Decision: "denied", Reason: "governance error: " + err.Error()}
	}
	switch dec.Decision {
	case "denied":
		return false, dec
	case "requires_approval":
		approved, ok := d.resolveApprovalOrEscalate(parent, parentTask, dec, "RemoteDispatch", resource, "remote dispatch ("+resource+")")
		if !ok {
			return false, dec
		}
		return true, approved
	default:
		return true, dec
	}
}

// runStreamingStepRemote executes st via the EXISTING cross-process
// dispatch/lease/report pipeline — go/scheduler/dispatch.go's
// SubmitForDispatchWithCapabilities/LeaseMatching/Report plus
// go/daemon/dispatch.go's work.poll/work.renew/work.report RPC handlers,
// which apps/carina-worker already implements end to end as a real,
// separately-running worker process — instead of the in-process
// spawnSubagentContextIDBound path every other streaming step uses. This is
// what actually lets a streaming-workflow step run on a different machine
// (the Agent Swarm design's P4 target): no new dispatch machinery, just
// wiring the streaming coordinator to reach the machinery that already
// exists. It blocks (subject to ctx cancellation) polling scheduler state
// until an external worker reports a terminal result, so it must be called
// from the same per-step goroutine dispatch() already spawns for local
// steps — no coordinator-side concurrency change needed, only which of the
// two execution paths runStreamingStep picks.
func (d *Daemon) runStreamingStepRemote(ctx context.Context, parent *sessionstore.Session, parentTask *scheduler.Task, spec *WorkflowSpec, runID string, st WorkflowStep, taskText string) streamingStepResult {
	resource := fmt.Sprintf("step:%s:workflow:%s", st.ID, spec.Name)
	if pool := st.Affinity["worker_pool"]; pool != "" {
		resource += ":pool:" + pool
	}
	allowed, dec := d.gateRemoteDispatch(parent, parentTask, resource)
	if !allowed {
		return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: "DENIED: " + dec.Reason, tokenUsageObserved: true}
	}

	var required []string
	if pool := st.Affinity["worker_pool"]; pool != "" {
		required = []string{"worker_pool:" + pool}
	}
	task := d.sched.SubmitForDispatchWithCapabilities(parent.SessionID, parent.WorkspaceID, taskText, nil, required)
	d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
		"status": "workflow_step_dispatched_remote", "workflow": spec.Name, "run_id": runID, "step": st.ID,
		"dispatch_task_id": task.TaskID, "required_worker_capabilities": required,
	}, dec.DecisionID)

	ticker := time.NewTicker(remoteDispatchPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_, _ = d.sched.Cancel(task.TaskID)
			return streamingStepResult{id: st.ID, kind: stepSkipped, errMsg: "workflow cancelled while waiting for a remote worker"}
		case <-ticker.C:
			t, ok := d.sched.Get(task.TaskID)
			if !ok {
				return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: "dispatched task disappeared from the scheduler"}
			}
			switch t.Status {
			case "completed":
				return streamingStepResult{id: st.ID, kind: stepDone, output: t.Summary}
			case "failed", "degraded":
				return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: fmt.Sprintf("remote worker reported %s: %s", t.Status, t.Summary)}
			case "cancelled":
				return streamingStepResult{id: st.ID, kind: stepSkipped, errMsg: "dispatched task was cancelled"}
			default:
				// "queued" (no worker has leased it yet) or "running"
				// (leased, still executing) — keep waiting. ReapExpiredLeases
				// (dispatch.go) already re-queues it if the leasing worker
				// dies without reporting, so this loop doesn't need its own
				// crash-recovery logic — it just observes scheduler state.
			}
		}
	}
}
