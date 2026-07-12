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

// generatorInjectionWindow/maxGeneratorNodesPerWindow bound the RATE of
// dynamic graph growth, independent of maxGeneratorDepth/
// maxStreamingWorkflowSteps above: a chain of otherwise-valid generators —
// each individually within the depth and total-step caps — could still
// inject in rapid bursts, a "graph-scale DoS" the design doc's own §13
// flagged as needing finer throttling than a hard cap alone provides
// ("每分钟最多注入 N 个节点"). See streamCoordinator.checkGeneratorInjectionRate.
const (
	generatorInjectionWindow   = 60 * time.Second
	maxGeneratorNodesPerWindow = 100
)

// swarmSpawnApprovalThreshold is the graph-size point past which every
// further generator injection re-triggers Capability::SwarmSpawn approval
// (design doc §11/§13's originally-proposed mitigation) — ordinary, small
// graphs never see this gate at all; once a run has grown large enough to
// matter, further unattended growth needs a decision, not silent continuation.
const swarmSpawnApprovalThreshold = maxStreamingWorkflowSteps / 2

// genInjectEvent records one generator injection for the sliding-window
// rate check — count, not just a timestamp, since one call can inject a
// batch of many nodes at once.
type genInjectEvent struct {
	at    time.Time
	count int
}

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
	id         string
	kind       stepOutcomeKind
	output     string
	errMsg     string
	tokensUsed int
	// tokenUsageObserved distinguishes a measured zero from an external
	// executor whose result schema carries no usage at all.
	tokenUsageObserved bool
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
	genOrigin  map[string]string
	generated  []persistedGeneratedStep

	// genInjectEvents is the sliding window checkGeneratorInjectionRate
	// reads/trims on every injection — deliberately NOT persisted across a
	// daemon restart (unlike sc.generated): the rate limit protects THIS
	// process's live scheduling loop from a burst, not the graph's
	// durable definition, so restarting with a fresh window is correct,
	// not a gap.
	genInjectEvents []genInjectEvent

	outputs      map[string]string
	terminal     map[string]stepOutcomeKind
	skipPending  map[string]bool
	liveIndegree map[string]int
	snapshot     map[string]stepResult

	// channels is one swarmChannelBroker shared by every step in THIS run,
	// giving steps that declare consumes_channel a way to receive live
	// messages from still-running siblings (see swarm_channel.go) — never
	// nil once run() has initialized it.
	channels    *swarmChannelBroker
	control     *workflowRunControl
	pausedReady map[string]bool

	totalSteps         int // grows if generator steps inject new nodes
	resolvedCount      int
	remainingToResolve int

	// Budget tree + observability rollup (Agent Swarm design §9/§10):
	// aggregate counters maintained incrementally (not recomputed by
	// scanning byID on every event) so they stay cheap at hundreds of
	// steps. budgetSpent accumulates every terminal step's tokensUsed
	// regardless of outcome — a step that burned tokens before failing
	// still counts against the run's budget. completed/failed/skipped are
	// FINAL-state counts; runningCount is steps currently dispatched and
	// awaiting a result (incremented in dispatch(), decremented at the top
	// of handleResult()).
	budgetSpent    int
	unmeteredCount int
	completedCount int
	failedCount    int
	skippedCount   int
	runningCount   int

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
	persistedGenerated, err := store.loadGenerated(runID)
	if err != nil {
		return nil, err
	}

	sc := &streamCoordinator{
		d: d, parent: parent, parentTask: parentTask, spec: spec, input: input, runID: runID, store: store,
		byID:         make(map[string]WorkflowStep, len(spec.Steps)),
		dependents:   make(map[string][]string, len(spec.Steps)),
		genDepth:     make(map[string]int, len(spec.Steps)),
		genOrigin:    make(map[string]string),
		outputs:      map[string]string{},
		terminal:     map[string]stepOutcomeKind{},
		skipPending:  map[string]bool{},
		liveIndegree: map[string]int{},
		snapshot:     map[string]stepResult{},
		skipQueue:    make([]string, 0, 8),
		channels:     newSwarmChannelBroker(),
		control:      d.workflowControl(runID),
		pausedReady:  map[string]bool{},
	}
	for _, st := range spec.Steps {
		sc.byID[st.ID] = st
	}
	if err := sc.restoreGeneratedSteps(persistedGenerated); err != nil {
		return nil, err
	}
	for _, st := range sc.byID {
		for _, n := range st.Needs {
			sc.dependents[n] = append(sc.dependents[n], st.ID)
		}
	}
	sc.totalSteps = len(sc.byID)
	if len(persistedGenerated) > 0 && d.workflowRuns != nil {
		steps := make([]workflowui.Step, 0, len(persistedGenerated))
		for _, generated := range persistedGenerated {
			steps = append(steps, workflowui.Step{ID: generated.Step.ID, DefinitionHash: workflowStepDefinitionHash(generated.Step)})
		}
		if _, managedErr := d.workflowRuns.Detail(runID); managedErr == nil {
			if _, err := d.workflowRuns.AddSteps(runID, steps); err != nil {
				return nil, fmt.Errorf("restore generated workflow UI steps: %w", err)
			}
		}
	}

	for id, r := range persisted {
		if r.Status == "completed" {
			if _, exists := sc.byID[id]; !exists {
				return nil, fmt.Errorf("workflow result journal contains unknown step %q; graph recovery is incomplete", id)
			}
			sc.outputs[id] = r.Output
			sc.terminal[id] = stepDone
			sc.snapshot[id] = r
			sc.resolvedCount++
			sc.completedCount++ // seed the rollup so a resumed run's counts include already-done steps, not just newly-resolved ones
		}
	}
	for _, st := range sc.byID {
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
	sc.remainingToResolve = sc.totalSteps - sc.resolvedCount

	d.record(parent.SessionID, "TaskCreated", parentTask.TaskID, "go", map[string]any{
		"status": "workflow_started", "workflow": spec.Name, "run_id": runID,
		"steps": sc.totalSteps, "execution_mode": "streaming", "resumed": sc.resolvedCount,
	}, "")

	concurrency := defaultStreamingWorkflowConcurrency
	if concurrency > sc.totalSteps {
		concurrency = sc.totalSteps
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

	for _, st := range sc.byID {
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
	var wake <-chan struct{}
	if sc.control != nil {
		wake = sc.control.wake
	}
	for sc.remainingToResolve > 0 {
		select {
		case <-sc.runCtx.Done():
			if sc.fatal == nil {
				sc.fatal = sc.runCtx.Err()
			}
			for id := range sc.byID {
				if _, terminal := sc.terminal[id]; !terminal {
					sc.markSkipped(id, "workflow stopped before this step reached a terminal state")
				}
			}
			return sc.outputs, sc.fatal
		case res := <-sc.results:
			sc.handleResult(res)
		case <-wake:
			sc.dispatchPausedReady()
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
	sc.runningCount++
	go func() {
		select {
		case sc.sem <- struct{}{}:
		case <-sc.runCtx.Done():
			sc.results <- streamingStepResult{id: id, kind: stepSkipped, errMsg: "workflow cancelled before this step started", tokenUsageObserved: true}
			return
		}
		defer func() { <-sc.sem }()
		sc.results <- sc.d.runStreamingStep(sc.runCtx, sc.parent, sc.parentTask, sc.spec, sc.runID, st, sc.input, inputSnapshot, sc.channels)
	}()
}

// maybeDispatch evaluates st.When (if set) against the current outputs
// before deciding whether to actually dispatch. A falsy/erroring condition
// resolves the step as skipped through the SAME propagation path an
// upstream failure uses — conditional branching and failure isolation share
// one mechanism.
func (sc *streamCoordinator) maybeDispatch(id string) {
	if sc.control != nil && sc.control.isPaused() {
		sc.pausedReady[id] = true
		return
	}
	// Budget-tree enforcement (design §9): once the run's aggregate spend
	// meets its declared ceiling, refuse to admit any NEW step — but never
	// kill a step already in flight (runningCount steps finish naturally;
	// see handleResult). Tokens are monotonic (never refunded), so "pause
	// until headroom frees up" isn't meaningful here the way it would be for
	// a renewable resource — the correct terminal behavior is to skip
	// remaining not-yet-dispatched steps with a clear, audited reason
	// rather than either silently truncating the run or aborting it outright.
	if sc.spec.TokenBudget > 0 && sc.budgetSpent >= sc.spec.TokenBudget {
		sc.markSkipped(id, fmt.Sprintf("workflow token budget exhausted (%d/%d tokens spent)", sc.budgetSpent, sc.spec.TokenBudget))
		return
	}
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

func (sc *streamCoordinator) dispatchPausedReady() {
	if sc.control != nil && sc.control.isPaused() {
		return
	}
	ready := make([]string, 0, len(sc.pausedReady))
	for id := range sc.pausedReady {
		ready = append(ready, id)
	}
	for _, id := range ready {
		delete(sc.pausedReady, id)
		if _, terminal := sc.terminal[id]; !terminal {
			sc.maybeDispatch(id)
		}
	}
}

func (sc *streamCoordinator) markSkipped(id, reason string) {
	sc.terminal[id] = stepSkipped
	sc.resolvedCount++
	sc.remainingToResolve--
	sc.skippedCount++
	sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", map[string]any{
		"status": "workflow_step_skipped", "workflow": sc.spec.Name, "run_id": sc.runID, "step": id, "reason": reason,
	}, "")
	// markSkipped resolves a step WITHOUT ever calling runStreamingStep (a
	// condition-false edge, a cascaded upstream failure, or an exhausted
	// run-level token budget), so — unlike every other terminal path, which
	// records its own workflowui.Step update — this is the only place that
	// needs to persist the step's live status itself. Without this, a
	// skipped step's operator-facing record would sit at its Create()-time
	// default of Queued forever, and workflow.detail's aggregate
	// (Completed+Failed+Skipped)/Total would never reach 100% for a run
	// that genuinely finished.
	sc.d.updateWorkflowUIStepBestEffort(sc.runID, workflowui.Step{ID: id, Status: workflowui.Skipped, Error: reason})
	sc.skipQueue = append(sc.skipQueue, id)
}

// rollupPayload is the aggregated, coordinator-side observability view
// (design §10): "N completed / M running / K failed / remaining budget",
// NOT a per-step firehose. This is what a large-scale (hundreds-of-steps)
// subscriber sees by default; per-step detail remains available exactly as
// before via the existing "workflow_step_*" events on the same session's
// event stream — a client that wants to drill into one step's full detail
// still can, but doesn't have to consume it just to watch overall progress.
func (sc *streamCoordinator) rollupPayload() map[string]any {
	queued := sc.totalSteps - sc.completedCount - sc.failedCount - sc.skippedCount - sc.runningCount
	if queued < 0 {
		queued = 0 // defensive: totalSteps can grow mid-run via generator injection between reads
	}
	payload := map[string]any{
		"total": sc.totalSteps, "completed": sc.completedCount, "failed": sc.failedCount,
		"skipped": sc.skippedCount, "running": sc.runningCount, "queued": queued,
		"budget_spent":          sc.budgetSpent,
		"budget_spent_observed": sc.budgetSpent,
	}
	if sc.unmeteredCount > 0 {
		payload["unmetered_steps"] = sc.unmeteredCount
		payload["budget_spent_is_complete"] = false
		payload["budget_enforcement"] = "observed_usage_only"
	} else {
		payload["budget_spent_is_complete"] = true
		payload["budget_enforcement"] = "complete"
	}
	if sc.spec.TokenBudget > 0 {
		remaining := sc.spec.TokenBudget - sc.budgetSpent
		if remaining < 0 {
			remaining = 0
		}
		payload["budget_limit"] = sc.spec.TokenBudget
		payload["budget_remaining"] = remaining
	}
	// Swarm channel activity (P3) was previously invisible to the P5
	// aggregate view entirely — a real observability gap, not just a
	// missing nice-to-have: an operator watching only the rollup stream had
	// no way to tell a swarm channel was even in use, let alone whether it
	// was healthy (steadily evicting under load) or quiet.
	if published, evicted := sc.channels.stats(); published > 0 {
		payload["channel_messages_published"] = published
		if evicted > 0 {
			payload["channel_messages_evicted"] = evicted
		}
	}
	return payload
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
	sc.runningCount-- // balances dispatch()'s increment — every dispatched step ends up here exactly once
	sc.budgetSpent += res.tokensUsed
	if !res.tokenUsageObserved {
		sc.unmeteredCount++
	}
	switch res.kind {
	case stepDone:
		if sc.byID[res.id].Kind == "generator" {
			if err := sc.injectGeneratedSteps(res.id, res.output); err != nil {
				sc.terminal[res.id] = stepFailed
				sc.failedCount++
				sc.d.updateWorkflowUIStepBestEffort(sc.runID, workflowui.Step{
					ID: res.id, Status: workflowui.Failed, Error: err.Error(), TokensUsed: int64(res.tokensUsed), TokenUsageStatus: observedUsageStatus(res.tokenUsageObserved),
				})
				sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", map[string]any{
					"status": "workflow_generator_invalid", "workflow": sc.spec.Name, "run_id": sc.runID,
					"step": res.id, "error": err.Error(),
				}, "")
				if sc.byID[res.id].FailFast {
					sc.fatal = fmt.Errorf("generator step %q: %w", res.id, err)
					sc.cancel()
					return
				}
				sc.propagate(res.id, true)
				break
			}
		}
		sc.terminal[res.id] = stepDone
		sc.completedCount++
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
		sc.propagate(res.id, false)
	case stepFailed:
		sc.terminal[res.id] = stepFailed
		sc.failedCount++
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
		sc.skippedCount++
		// Covers dispatch()'s own pre-execution cancellation path (a step
		// cancelled while still waiting for a worker-pool slot, before
		// runStreamingStep ever ran) — the ONLY stepSkipped source that
		// isn't already covered by runStreamingStep's own
		// updateWorkflowUIStepBestEffort call or markSkipped's.
		sc.d.updateWorkflowUIStepBestEffort(sc.runID, workflowui.Step{ID: res.id, Status: workflowui.Skipped, Error: res.errMsg})
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
	rollup := sc.rollupPayload()
	rollup["status"] = "workflow_progress_rollup"
	rollup["workflow"] = sc.spec.Name
	rollup["run_id"] = sc.runID
	sc.d.record(sc.parent.SessionID, "TaskCreated", sc.parentTask.TaskID, "go", rollup, "")
}

// runStreamingStep executes exactly one step (interpolation, structured
// Input resolution, spawn, telemetry, operator state), mirroring
// runWorkflow's per-step body, and reports the outcome on the caller-owned
// results channel instead of mutating shared state directly — this function
// touches no graph bookkeeping at all.
func (d *Daemon) runStreamingStep(ctx context.Context, parent *sessionstore.Session, parentTask *scheduler.Task, spec *WorkflowSpec, runID string, st WorkflowStep, input string, outputsSoFar map[string]string, channels *swarmChannelBroker) streamingStepResult {
	if err := ctx.Err(); err != nil {
		return streamingStepResult{id: st.ID, kind: stepSkipped, errMsg: "workflow cancelled", tokenUsageObserved: true}
	}
	startedAt := time.Now().UTC()
	if d.workflowRuns != nil {
		now := startedAt
		if detail, managedErr := d.workflowRuns.Detail(runID); managedErr == nil && workflowRunAcceptsStepUpdate(detail.Run.Status) {
			if _, err := d.workflowRuns.UpdateStep(runID, workflowui.Step{ID: st.ID, Status: workflowui.Running, StartedAt: &now}); err != nil {
				return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: fmt.Sprintf("refused before execution because running state could not persist: %v", err), tokenUsageObserved: true}
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
	taskText += swarmChannelInstructionSuffix(st.ConsumesChannel)

	// Remote or affinity-tagged steps skip the in-process subagent path
	// entirely — swarm channel binding, "ToolApproved" recording, and
	// telemetry are handled inside runStreamingStepRemote/its own dispatch
	// bookkeeping, since the execution itself happens in a different
	// process (see workflow_remote.go).
	if st.Remote || len(st.Affinity) > 0 {
		res := d.runStreamingStepRemote(ctx, parent, parentTask, spec, runID, st, taskText, channels)
		if d.workflowRuns != nil {
			now := time.Now().UTC()
			if detail, managedErr := d.workflowRuns.Detail(runID); managedErr == nil && workflowRunAcceptsStepUpdate(detail.Run.Status) {
				uiStatus := workflowui.Completed
				if res.kind == stepFailed {
					uiStatus = workflowui.Failed
				} else if res.kind == stepSkipped {
					uiStatus = workflowui.Skipped
				}
				_, _ = d.workflowRuns.UpdateStep(runID, workflowui.Step{ID: st.ID, Status: uiStatus, FinishedAt: &now, Output: res.output, Error: res.errMsg, TokensUsed: int64(res.tokensUsed), TokenUsageStatus: observedUsageStatus(res.tokenUsageObserved)})
			}
		}
		outcome := "failed"
		if res.kind == stepDone {
			outcome = "completed"
		}
		_ = d.telemetry.Span("carina.workflow.step", runID, st.ID, carinatelemetry.Attribution{
			WorkspaceID: parent.WorkspaceID, SessionID: parent.SessionID, WorkflowID: runID,
			StepID: st.ID, TaskID: parentTask.TaskID,
		}, carinatelemetry.Cost{}, time.Since(startedAt), outcome)
		return res
	}

	d.record(parent.SessionID, "ToolApproved", parentTask.TaskID, "go", map[string]any{
		"workflow": spec.Name, "run_id": runID, "step": st.ID, "agent": st.Agent, "execution_mode": "streaming",
	}, "")

	binding := &swarmChannelBinding{broker: channels, stepID: st.ID, subscribed: st.ConsumesChannel}
	summary, childSessionID := d.spawnSubagentContextIDBound(ctx, parent, parentTask, st.Agent, taskText, binding)
	// Attribute this step's cost to the run's aggregate budget (design §9)
	// regardless of outcome — a step that burned tokens before failing
	// still counts. childSessionID is "" if spawn was refused before a
	// child session ever existed (denied capability, unknown agent, etc.),
	// in which case there is nothing to look up and cost is correctly 0.
	tokensUsed := 0
	if childSessionID != "" {
		tokensUsed = d.sessionTotalTokens(childSessionID)
	}
	if err := ctx.Err(); err != nil {
		// workflowui recording for every stepSkipped outcome — this one and
		// dispatch()'s own pre-execution cancellation path alike — is
		// centralized in the coordinator's handleResult (both flow through
		// the same streamingStepResult{kind: stepSkipped} shape), so nothing
		// to do here beyond returning it.
		return streamingStepResult{id: st.ID, kind: stepSkipped, errMsg: "workflow cancelled", tokensUsed: tokensUsed, tokenUsageObserved: true}
	}
	if spawnFailed(summary) {
		_ = d.telemetry.Span("carina.workflow.step", runID, st.ID, carinatelemetry.Attribution{
			WorkspaceID: parent.WorkspaceID, SessionID: parent.SessionID, WorkflowID: runID,
			StepID: st.ID, TaskID: parentTask.TaskID,
		}, carinatelemetry.Cost{}, time.Since(startedAt), "failed")
		// Found via a real end-to-end smoke test (carina workflow run against
		// a live daemon, no LLM provider configured): a step whose SPAWN
		// itself fails used to leave its workflowui.Step permanently stuck at
		// the "Running" status set above, even though the coordinator's own
		// graph state correctly resolved it as failed and cascaded skips to
		// its dependents — workflow.detail/`carina workflow status` showed a
		// "completed" run with one step frozen at "running" forever. Same bug
		// class as markSkipped's fix above, just a different code path that
		// missed it.
		d.updateWorkflowUIStepBestEffort(runID, workflowui.Step{ID: st.ID, Status: workflowui.Failed, Error: summary, TokensUsed: int64(tokensUsed), TokenUsageStatus: "observed"})
		return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: summary, tokensUsed: tokensUsed, tokenUsageObserved: true}
	}

	if d.workflowRuns != nil {
		now := time.Now().UTC()
		detail, managedErr := d.workflowRuns.Detail(runID)
		if managedErr == nil && workflowRunAcceptsStepUpdate(detail.Run.Status) {
			if _, err := d.workflowRuns.UpdateStep(runID, workflowui.Step{ID: st.ID, Status: workflowui.Completed, FinishedAt: &now, Output: summary, TokensUsed: int64(tokensUsed), TokenUsageStatus: "observed"}); err != nil {
				return streamingStepResult{id: st.ID, kind: stepFailed, errMsg: fmt.Sprintf("completed but operator state persistence failed: %v", err), tokensUsed: tokensUsed, tokenUsageObserved: true}
			}
		}
	}
	_ = d.telemetry.Span("carina.workflow.step", runID, st.ID, carinatelemetry.Attribution{
		WorkspaceID: parent.WorkspaceID, SessionID: parent.SessionID, WorkflowID: runID,
		StepID: st.ID, TaskID: parentTask.TaskID,
	}, carinatelemetry.Cost{}, time.Since(startedAt), "completed")
	return streamingStepResult{id: st.ID, kind: stepDone, output: summary, tokensUsed: tokensUsed, tokenUsageObserved: true}
}

func observedUsageStatus(observed bool) string {
	if observed {
		return "observed"
	}
	return "unavailable_remote"
}

// updateWorkflowUIStepBestEffort records a step's terminal workflowui.Step
// status for an ALREADY-bad outcome (failed, cancelled/skipped) — unlike the
// success path above, a persistence failure here doesn't get escalated into
// changing the step's own outcome (it's already terminal-bad; there's
// nothing better to escalate it TO), just best-effort recorded so
// workflow.detail/`carina workflow status` don't show it frozen at
// "running" forever. Sets FinishedAt so a caller can tell it actually
// resolved, not merely that step.Status differs from the default.
func (d *Daemon) updateWorkflowUIStepBestEffort(runID string, step workflowui.Step) {
	if d.workflowRuns == nil {
		return
	}
	detail, managedErr := d.workflowRuns.Detail(runID)
	if managedErr != nil || (!workflowRunAcceptsStepUpdate(detail.Run.Status) && step.Status != workflowui.Skipped) {
		return
	}
	now := time.Now().UTC()
	step.FinishedAt = &now
	_, _ = d.workflowRuns.UpdateStep(runID, step)
}

func workflowRunAcceptsStepUpdate(status workflowui.Status) bool {
	return status != workflowui.Stopped && status != workflowui.Interrupted && status != workflowui.Completed && status != workflowui.Failed
}

// sessionTotalTokens sums TokensUsed across every scheduler task ever
// created under sessionID (normally exactly one, for a spawned subagent
// child session) — used to attribute a completed streaming-workflow step's
// cost to its run's aggregate budget (streamCoordinator.budgetSpent).
func (d *Daemon) sessionTotalTokens(sessionID string) int {
	total := 0
	for _, t := range d.sched.List() {
		if t.SessionID == sessionID {
			total += t.TokensUsed
		}
	}
	return total
}
