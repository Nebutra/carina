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
// handle well. It bounds BOTH the step count declared up front and the
// cumulative total after any generator-node injection (see
// workflow_generator.go) — a hard ceiling either way, not unbounded.
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

// maxGeneratorDepth bounds how many generator-step "generations" deep a
// dynamically-injected step chain can go (a generator injecting a step that
// is itself a generator, and so on) — independent of the total step-count
// ceiling, this specifically prevents an unbounded chain of single-step
// generations from being technically under the step cap at every instant
// while still running forever.
const maxGeneratorDepth = 5

// stepOutcomeKind is the terminal state of one streaming-mode step.
// stepUnresolved is deliberately the zero value: several maps below are read
// with a plain equality check (not the two-value `v, ok := ...` form), and a
// Go map lookup miss returns the zero value of the value type — if stepDone
// were the zero value instead, every not-yet-resolved dependency would
// silently read back as "already done". This is not hypothetical: it was a
// real bug in this file's first draft, caught by two tests failing.
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

// streamCoordinator owns ALL streaming-workflow graph state (dependency
// counts, terminal status, outputs) in one place, mutated only from the
// single goroutine that calls run() — worker goroutines only execute a
// step's actual work and report back over the results channel, never touch
// this struct's fields directly. This is what lets the dependency graph
// itself go without a mutex: only the channel crosses goroutines.
type streamCoordinator struct {
	d          *Daemon
	parent     *sessionstore.Session
	parentTask *scheduler.Task
	spec       *WorkflowSpec
	input      string
	runID      string
	store      *wfRunStore

	byID       map[string]WorkflowStep
	dependents map[string][]string
	genDepth   map[string]int

	outputs      map[string]string
	terminal     map[string]stepOutcomeKind
	skipPending  map[string]bool
	liveIndegree map[string]int
	snapshot     map[string]stepResult

	totalSteps         int // grows if generator steps inject new nodes
	resolvedCount      int
	remainingToResolve int

	runCtx  context.Context
	cancel  context.CancelFunc
	sem     chan struct{}
	results chan streamingStepResult

	fatal     error
	skipQueue []string
}

// runWorkflowStreaming is runWorkflow's streaming-scheduler sibling (see
// WorkflowSpec.ExecutionMode). Unlike the batch/BSP scheduler — which
// collects every step whose dependencies are satisfied, waits for the WHOLE
// batch to finish, then computes the next batch — this dispatches a step the
// INSTANT its own dependencies resolve, independent of how long sibling
// steps happen to take, bounds actual concurrent execution with a fixed
// worker pool, evaluates per-step When conditions, resolves structured
// Input, and lets "generator" steps inject new nodes into the still-running
// graph.
//
// Failure semantics default to isolate: a failed (or condition-skipped) step
// marks only its transitive-only dependents as skipped, independent
// branches keep going. A step opts into the old "abort the whole run"
// behavior via WorkflowStep.FailFast.
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

	sc := &streamCoordinator{
		d: d, parent: parent, parentTask: parentTask, spec: spec, input: input, runID: runID, store: store,
		byID:         make(map[string]WorkflowStep, len(spec.Steps)),
		dependents:   make(map[string][]string, len(spec.Steps)),
		genDepth:     make(map[string]int, len(spec.Steps)),
		outputs:      map[string]string{},
		terminal:     map[string]stepOutcomeKind{},
		skipPending:  map[string]bool{},
		liveIndegree: map[string]int{},
		snapshot:     map[string]stepResult{},
		skipQueue:    make([]string, 0, 8),
	}
	for _, st := range spec.Steps {
		sc.byID[st.ID] = st
	}
	for _, st := range spec.Steps {
		for _, n := range st.Needs {
			sc.dependents[n] = append(sc.dependents[n], st.ID)
		}
	}
	sc.totalSteps = len(spec.Steps)

	for id, r := range persisted {
		if r.Status == "completed" {
			sc.outputs[id] = r.Output
			sc.terminal[id] = stepDone
			sc.snapshot[id] = r
			sc.resolvedCount++
		}
	}
	for _, st := range spec.Steps {
		if _, done := sc.terminal[st.ID]; done {
			continue
		}
		n := 0
		for _, dep := range st.Needs {
			if sc.terminal[dep] != stepDone {
				n++
			}
		}
		sc.liveIndegree[st.ID] = n
	}
	sc.remainingToResolve = len(spec.Steps) - sc.resolvedCount

	d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
		"status": "workflow_started", "workflow": spec.Name, "run_id": runID,
		"steps": len(spec.Steps), "execution_mode": "streaming", "resumed": sc.resolvedCount,
	}, "")

	concurrency := defaultStreamingWorkflowConcurrency
	if concurrency > len(spec.Steps) {
		concurrency = len(spec.Steps)
	}
	if concurrency < 1 {
		concurrency = 1
	}

	sc.runCtx, sc.cancel = context.WithCancel(ctx)
	defer sc.cancel()
	sc.sem = make(chan struct{}, concurrency)
	// Sized to the hard ceiling, not the initial step count: generator steps
	// can grow totalSteps at runtime (bounded by maxStreamingWorkflowSteps),
	// and a buffered channel's capacity can't be changed after creation.
	sc.results = make(chan streamingStepResult, maxStreamingWorkflowSteps)

	for _, st := range spec.Steps {
		if _, done := sc.terminal[st.ID]; done {
			continue
		}
		if sc.liveIndegree[st.ID] == 0 {
			sc.maybeDispatch(st.ID)
		}
	}

	return sc.run()
}

func (sc *streamCoordinator) run() (map[string]string, error) {
	for sc.remainingToResolve > 0 {
		select {
		case <-sc.runCtx.Done():
			if sc.fatal == nil {
				sc.fatal = sc.runCtx.Err()
			}
			return sc.outputs, sc.fatal
		case res := <-sc.results:
			sc.handleResult(res)
		}
	}

	if sc.fatal != nil {
		sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", map[string]any{
			"status": "workflow_failed", "workflow": sc.spec.Name, "run_id": sc.runID, "error": sc.fatal.Error(),
		}, "")
		return sc.outputs, sc.fatal
	}

	sc.d.record(sc.parent.SessionID, "ModelResponded", sc.parentTask.TaskID, "go", map[string]any{
		"status": "workflow_completed", "workflow": sc.spec.Name, "run_id": sc.runID, "steps": len(sc.outputs),
	}, "")
	return sc.outputs, nil
}

// dispatch spawns exactly one step's execution, bounded by sc.sem, and is
// only ever called from the coordinator goroutine — the snapshot below is
// what makes that safe: sc.outputs is a live map the coordinator keeps
// mutating as steps complete, and Go maps are not safe for concurrent
// read/write, so each dispatched step gets an immutable point-in-time copy
// taken HERE rather than a reference a spawned goroutine could read while
// the coordinator writes to it.
func (sc *streamCoordinator) dispatch(id string) {
	inputSnapshot := make(map[string]string, len(sc.outputs))
	for k, v := range sc.outputs {
		inputSnapshot[k] = v
	}
	st := sc.byID[id]
	go func() {
		select {
		case sc.sem <- struct{}{}:
		case <-sc.runCtx.Done():
			sc.results <- streamingStepResult{id: id, kind: stepSkipped, errMsg: "workflow cancelled before this step started"}
			return
		}
		defer func() { <-sc.sem }()
		sc.results <- sc.d.runStreamingStep(sc.runCtx, sc.parent, sc.parentTask, sc.spec, sc.runID, st, sc.input, inputSnapshot)
	}()
}

// maybeDispatch evaluates st.When (if set) against the current outputs
// before deciding whether to actually dispatch. A falsy/erroring condition
// resolves the step as skipped through the SAME propagation path an
// upstream failure uses — conditional branching and failure isolation share
// one mechanism.
func (sc *streamCoordinator) maybeDispatch(id string) {
	st := sc.byID[id]
	if len(st.When) > 0 {
		data := make(map[string]any, len(sc.outputs))
		for k, v := range sc.outputs {
			data[k] = parseStepOutputAsData(v)
		}
		ok, err := evalCondition(st.When, data)
		if err != nil || !ok {
			reason := "condition not satisfied"
			if err != nil {
				reason = "condition error (fails closed): " + err.Error()
			}
			sc.markSkipped(id, reason)
			return
		}
	}
	sc.dispatch(id)
}

func (sc *streamCoordinator) markSkipped(id, reason string) {
	sc.terminal[id] = stepSkipped
	sc.resolvedCount++
	sc.remainingToResolve--
	sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", map[string]any{
		"status": "workflow_step_skipped", "workflow": sc.spec.Name, "run_id": sc.runID, "step": id, "reason": reason,
	}, "")
	sc.skipQueue = append(sc.skipQueue, id)
}

// propagate is called once a step (fromID) has resolved, decrementing every
// dependent's live in-degree and — if upstreamBad — marking them as skip
// candidates. A dependent whose in-degree reaches zero is either resolved as
// skipped (if any upstream was bad) or handed to maybeDispatch.
func (sc *streamCoordinator) propagate(fromID string, upstreamBad bool) {
	for _, depID := range sc.dependents[fromID] {
		if _, done := sc.terminal[depID]; done {
			continue
		}
		if upstreamBad {
			sc.skipPending[depID] = true
		}
		sc.liveIndegree[depID]--
		if sc.liveIndegree[depID] == 0 {
			if sc.skipPending[depID] {
				sc.markSkipped(depID, "an upstream dependency failed or was skipped")
			} else {
				sc.maybeDispatch(depID)
			}
		}
	}
}

func (sc *streamCoordinator) handleResult(res streamingStepResult) {
	if _, already := sc.terminal[res.id]; already {
		return // a dispatch raced a cancellation-triggered skip; ignore the late duplicate
	}
	sc.resolvedCount++
	sc.remainingToResolve--
	switch res.kind {
	case stepDone:
		sc.terminal[res.id] = stepDone
		sc.outputs[res.id] = res.output
		sc.snapshot[res.id] = stepResult{Status: "completed", Output: res.output}
		full := make(map[string]stepResult, len(sc.snapshot))
		for k, v := range sc.snapshot {
			full[k] = v
		}
		if err := sc.store.save(sc.runID, full); err != nil {
			sc.fatal = fmt.Errorf("step %q side effect completed but result persistence failed; manual reconciliation required: %w", res.id, err)
			sc.cancel()
			return
		}
		if sc.byID[res.id].Kind == "generator" {
			if err := sc.injectGeneratedSteps(res.id, res.output); err != nil {
				// A generator producing a bad envelope is a step-level
				// failure, not a whole-run fatal error by default — same
				// isolate posture as any other step, unless it opted into
				// FailFast.
				sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", map[string]any{
					"status": "workflow_generator_invalid", "workflow": sc.spec.Name, "run_id": sc.runID,
					"step": res.id, "error": err.Error(),
				}, "")
				if sc.byID[res.id].FailFast {
					sc.fatal = fmt.Errorf("generator step %q: %w", res.id, err)
					sc.cancel()
					return
				}
			}
		}
		sc.propagate(res.id, false)
	case stepFailed:
		sc.terminal[res.id] = stepFailed
		sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", map[string]any{
			"status": "workflow_step_failed", "workflow": sc.spec.Name, "run_id": sc.runID, "step": res.id,
			"error": res.errMsg, "fail_fast": sc.byID[res.id].FailFast,
		}, "")
		if sc.byID[res.id].FailFast {
			sc.fatal = fmt.Errorf("step %q: %s", res.id, res.errMsg)
			sc.cancel()
			return
		}
		sc.propagate(res.id, true)
	case stepSkipped:
		sc.terminal[res.id] = stepSkipped
		sc.propagate(res.id, true)
	}
	// Drain any skip-chain reactions queued by propagate()/markSkipped()
	// above before the next select iteration, so a long chain of dependents
	// resolves without waiting for spurious channel wakeups.
	for len(sc.skipQueue) > 0 {
		id := sc.skipQueue[0]
		sc.skipQueue = sc.skipQueue[1:]
		sc.propagate(id, true)
	}
}

// runStreamingStep executes exactly one step (interpolation, structured
// Input resolution, spawn, telemetry, operator state), mirroring
// runWorkflow's per-step body, and reports the outcome on the caller-owned
// results channel instead of mutating shared state directly — this function
// touches no graph bookkeeping at all.
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
	// dispatch time — never the live shared map, which the coordinator keeps
	// mutating concurrently with this goroutine running.
	taskText := interpolateStreaming(st.Task, input, outputsSoFar)
	if len(st.Input) > 0 {
		if block, err := formatStructuredInput(st.Input, input, outputsSoFar); err == nil {
			taskText += block
		}
	}
	if st.Kind == "generator" {
		taskText += generatorInstructionSuffix
	}

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
