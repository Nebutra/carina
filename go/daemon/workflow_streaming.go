package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	carinatelemetry "github.com/Nebutra/carina/go/telemetry"
	"github.com/Nebutra/carina/go/workflowui"
)

// maxStreamingWorkflowSteps is deliberately far above maxWorkflowSteps (64):
// the whole point of streaming mode is graphs the batch/BSP scheduler can't
// handle well. It is still a hard ceiling, not unbounded — see
// docs/plans/2026-07-12-agent-swarm-dag-orchestration-design.md §4.3 on why
// dynamic node injection (a later phase) needs its own separate cap
// discipline; this one only bounds what a caller can declare up front.
const maxStreamingWorkflowSteps = 1000

// defaultStreamingWorkflowConcurrency bounds how many steps run at once for a
// single streaming workflow run, replacing the unbounded "one goroutine per
// ready step" pattern the batch scheduler and subagent.go's parallel spawn
// fan-out both use today. This is deliberately higher than the daemon-wide
// d.runSem default (8): that semaphore still gates actual background *task*
// concurrency at the daemon level, but within one workflow run's subagent
// spawns, bounding fan-out here is what actually prevents "1000 ready steps
// = 1000 goroutines at once" — the failure mode this phase exists to fix.
const defaultStreamingWorkflowConcurrency = 16

// stepOutcomeKind is the terminal state of one streaming-mode step.
// stepUnresolved is deliberately the zero value: the `terminal` map is read
// with a plain `terminal[id]` equality check (not the two-value `v, ok :=
// ...` form) when computing initial in-degree, and a Go map lookup miss
// returns the zero value of the value type — if stepDone were the zero
// value instead, every not-yet-resolved dependency would silently read back
// as "already done", making every step's live in-degree compute as 0
// immediately. This is not hypothetical: it was the actual bug in this
// file's first draft, caught by TestWorkflowStreamingIsolatesFailureToItsOwnDependents
// and TestWorkflowStreamingDoesNotBarrierOnSlowSibling both failing.
type stepOutcomeKind int

const (
	stepUnresolved stepOutcomeKind = iota
	stepDone
	stepFailed
	stepSkipped
)

type streamingStepResult struct {
	id     string
	kind   stepOutcomeKind
	output string
	errMsg string
}

// runWorkflowStreaming is runWorkflow's streaming-scheduler sibling (see
// WorkflowSpec.ExecutionMode). Unlike the batch/BSP scheduler — which
// collects every step whose dependencies are satisfied, waits for the WHOLE
// batch to finish, then computes the next batch — this dispatches a step the
// INSTANT its own dependencies resolve, independent of how long sibling
// steps happen to take, and bounds actual concurrent execution with a fixed
// worker pool instead of one goroutine per ready step. A single graph state
// mutation site (this function's own goroutine, reading streamingStepResult
// off a channel) owns all bookkeeping, so no mutex is needed for the
// dependency graph itself — only the result channel crosses goroutines.
//
// Failure semantics default to isolate: a failed step marks only its
// transitive-only dependents as skipped (not run, not counted as failures),
// independent branches keep going. A step can opt into the old "abort the
// whole run" behavior via WorkflowStep.FailFast.
func (d *Daemon) runWorkflowStreaming(parent *sessionstore.Session, parentTask *scheduler.Task, spec *WorkflowSpec, input, runID string) (map[string]string, error) {
	ctx := d.contextForTask(parentTask.TaskID)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	if len(spec.Steps) > maxStreamingWorkflowSteps {
		return nil, fmt.Errorf("workflow %q exceeds the %d-step streaming limit", spec.Name, maxStreamingWorkflowSteps)
	}

	store := newWFRunStore(d.stateDir)
	persisted, err := store.load(runID)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]WorkflowStep, len(spec.Steps))
	dependents := make(map[string][]string, len(spec.Steps))
	for _, st := range spec.Steps {
		byID[st.ID] = st
	}
	for _, st := range spec.Steps {
		for _, n := range st.Needs {
			dependents[n] = append(dependents[n], st.ID)
		}
	}

	outputs := map[string]string{}
	terminal := map[string]stepOutcomeKind{}
	skipPending := map[string]bool{}
	liveIndegree := map[string]int{}
	snapshot := map[string]stepResult{}
	resolvedCount := 0

	for id, r := range persisted {
		if r.Status == "completed" {
			outputs[id] = r.Output
			terminal[id] = stepDone
			snapshot[id] = r
			resolvedCount++
		}
	}
	for _, st := range spec.Steps {
		if _, done := terminal[st.ID]; done {
			continue
		}
		n := 0
		for _, dep := range st.Needs {
			if terminal[dep] != stepDone {
				n++
			}
		}
		liveIndegree[st.ID] = n
	}

	d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
		"status": "workflow_started", "workflow": spec.Name, "run_id": runID,
		"steps": len(spec.Steps), "execution_mode": "streaming", "resumed": resolvedCount,
	}, "")

	concurrency := defaultStreamingWorkflowConcurrency
	if concurrency > len(spec.Steps) {
		concurrency = len(spec.Steps)
	}
	if concurrency < 1 {
		concurrency = 1
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, concurrency)
	results := make(chan streamingStepResult, len(spec.Steps))

	// dispatch is only ever called from this function's own goroutine (the
	// coordinator), both in the initial seeding loop and from propagate()
	// inside the results-draining loop below — never concurrently with
	// itself. That's what makes the snapshot below safe: outputs is a live
	// map the coordinator keeps writing to as steps complete, and Go maps are
	// not safe for concurrent read/write, so each dispatched step gets an
	// immutable point-in-time copy taken HERE (single-threaded) rather than a
	// reference to the shared map a spawned goroutine could read while the
	// coordinator writes to it.
	dispatch := func(id string) {
		inputSnapshot := make(map[string]string, len(outputs))
		for k, v := range outputs {
			inputSnapshot[k] = v
		}
		go func() {
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				results <- streamingStepResult{id: id, kind: stepSkipped, errMsg: "workflow cancelled before this step started"}
				return
			}
			defer func() { <-sem }()
			results <- d.runStreamingStep(runCtx, parent, parentTask, spec, runID, byID[id], input, inputSnapshot)
		}()
	}

	remainingToResolve := len(spec.Steps) - resolvedCount
	for _, st := range spec.Steps {
		if _, done := terminal[st.ID]; done {
			continue
		}
		if liveIndegree[st.ID] == 0 {
			dispatch(st.ID)
		}
	}

	var fatal error
	skipQueue := make([]string, 0, 8)
	propagate := func(fromID string, upstreamBad bool) {
		for _, depID := range dependents[fromID] {
			if _, done := terminal[depID]; done {
				continue
			}
			if upstreamBad {
				skipPending[depID] = true
			}
			liveIndegree[depID]--
			if liveIndegree[depID] == 0 {
				if skipPending[depID] {
					terminal[depID] = stepSkipped
					resolvedCount++
					remainingToResolve--
					d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
						"status": "workflow_step_skipped", "workflow": spec.Name, "run_id": runID, "step": depID,
						"reason": "an upstream dependency failed or was skipped",
					}, "")
					skipQueue = append(skipQueue, depID)
				} else {
					dispatch(depID)
				}
			}
		}
	}

	for remainingToResolve > 0 {
		select {
		case <-runCtx.Done():
			if fatal == nil {
				fatal = runCtx.Err()
			}
			return outputs, fatal
		case res := <-results:
			if _, already := terminal[res.id]; already {
				continue // a dispatch raced a cancellation-triggered skip; ignore the late duplicate result
			}
			resolvedCount++
			remainingToResolve--
			switch res.kind {
			case stepDone:
				terminal[res.id] = stepDone
				outputs[res.id] = res.output
				snapshot[res.id] = stepResult{Status: "completed", Output: res.output}
				full := make(map[string]stepResult, len(snapshot))
				for k, v := range snapshot {
					full[k] = v
				}
				if err := store.save(runID, full); err != nil {
					fatal = fmt.Errorf("step %q side effect completed but result persistence failed; manual reconciliation required: %w", res.id, err)
					cancel()
				}
				propagate(res.id, false)
			case stepFailed:
				terminal[res.id] = stepFailed
				d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
					"status": "workflow_step_failed", "workflow": spec.Name, "run_id": runID, "step": res.id,
					"error": res.errMsg, "fail_fast": byID[res.id].FailFast,
				}, "")
				if byID[res.id].FailFast {
					fatal = fmt.Errorf("step %q: %s", res.id, res.errMsg)
					cancel()
				} else {
					propagate(res.id, true)
				}
			case stepSkipped:
				terminal[res.id] = stepSkipped
				propagate(res.id, true)
			}
			// Drain any skip-chain reactions from propagate() above before the
			// next select iteration, so a long chain of dependents resolves
			// without waiting for spurious channel wakeups.
			for len(skipQueue) > 0 {
				id := skipQueue[0]
				skipQueue = skipQueue[1:]
				propagate(id, true)
			}
		}
	}

	if fatal != nil {
		d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
			"status": "workflow_failed", "workflow": spec.Name, "run_id": runID, "error": fatal.Error(),
		}, "")
		return outputs, fatal
	}

	d.record(parent.SessionID, "ModelResponded", parentTask.TaskID, "go", map[string]any{
		"status": "workflow_completed", "workflow": spec.Name, "run_id": runID, "steps": len(outputs),
	}, "")
	return outputs, nil
}

// runStreamingStep executes exactly one step (spawn + telemetry + operator
// state), mirroring runWorkflow's per-step body, and reports the outcome on
// the caller-owned results channel instead of mutating shared state directly
// — this function touches no graph bookkeeping at all.
func (d *Daemon) runStreamingStep(ctx context.Context, parent *sessionstore.Session, parentTask *scheduler.Task, spec *WorkflowSpec, runID string, st WorkflowStep, input string, outputsSoFar map[string]string) streamingStepResult {
	if err := ctx.Err(); err != nil {
		return streamingStepResult{id: st.ID, kind: stepSkipped, errMsg: "workflow cancelled"}
	}
	startedAt := time.Now().UTC()
	if d.workflowRuns != nil {
		now := startedAt
		if _, managedErr := d.workflowRuns.Detail(runID); managedErr == nil {
			if _, err := d.workflowRuns.UpdateStep(runID, workflowui.Step{ID: st.ID, Status: workflowui.Running, StartedAt: &now}); err != nil {
				return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: fmt.Sprintf("refused before execution because running state could not persist: %v", err)}
			}
		}
	}

	// outputsSoFar is an immutable snapshot taken by the coordinator at
	// dispatch time (see dispatch() in runWorkflowStreaming) — never the live
	// shared map, which the coordinator keeps mutating concurrently with this
	// goroutine running.
	taskText := interpolate(st.Task, input, outputsSoFar)

	d.record(parent.SessionID, "ToolApproved", parentTask.TaskID, "go", map[string]any{
		"workflow": spec.Name, "run_id": runID, "step": st.ID, "agent": st.Agent, "execution_mode": "streaming",
	}, "")

	summary := d.spawnSubagentContext(ctx, parent, parentTask, st.Agent, taskText)
	if err := ctx.Err(); err != nil {
		return streamingStepResult{id: st.ID, kind: stepSkipped, errMsg: "workflow cancelled"}
	}
	if spawnFailed(summary) {
		_ = d.telemetry.Span("carina.workflow.step", runID, st.ID, carinatelemetry.Attribution{
			WorkspaceID: parent.WorkspaceID, SessionID: parent.SessionID, WorkflowID: runID,
			StepID: st.ID, TaskID: parentTask.TaskID,
		}, carinatelemetry.Cost{}, time.Since(startedAt), "failed")
		return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: summary}
	}

	if d.workflowRuns != nil {
		now := time.Now().UTC()
		_, managedErr := d.workflowRuns.Detail(runID)
		if _, err := d.workflowRuns.UpdateStep(runID, workflowui.Step{ID: st.ID, Status: workflowui.Completed, FinishedAt: &now, Output: summary}); managedErr == nil && err != nil {
			return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: fmt.Sprintf("completed but operator state persistence failed: %v", err)}
		}
	}
	_ = d.telemetry.Span("carina.workflow.step", runID, st.ID, carinatelemetry.Attribution{
		WorkspaceID: parent.WorkspaceID, SessionID: parent.SessionID, WorkflowID: runID,
		StepID: st.ID, TaskID: parentTask.TaskID,
	}, carinatelemetry.Cost{}, time.Since(startedAt), "completed")
	return streamingStepResult{id: st.ID, kind: stepDone, output: summary}
}
