package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// taskEchoReasoner finishes immediately, echoing the step's TASK line into the
// done summary. Because its reply depends only on the prompt (not call order),
// parallel workflow steps are deterministic to assert on. Stateless => safe for
// concurrent Think calls.
type taskEchoReasoner struct{}

func (taskEchoReasoner) Name() string { return "task-echo" }
func (taskEchoReasoner) Think(_ context.Context, prompt string) (string, error) {
	task := ""
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "TASK: ") {
			task = strings.TrimPrefix(line, "TASK: ")
			break
		}
	}
	b, _ := json.Marshal(map[string]string{"tool": "done", "summary": "did[" + task + "]"})
	return string(b), nil
}

func writeWorkerAgent(t *testing.T, ws string) {
	t.Helper()
	dir := filepath.Join(ws, ".carina", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := "---\nname: worker\ndescription: run one step\nprofile: read-only\nmax_turns: 2\n---\nYou are a worker. Do the step, then finish with done.\n"
	if err := os.WriteFile(filepath.Join(dir, "worker.md"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseWorkflowSpec: JSON round-trips into a spec with steps + deps.
func TestParseWorkflowSpec(t *testing.T) {
	raw := []byte(`{"name":"rev","description":"d","steps":[
		{"id":"find","agent":"scout","task":"list files"},
		{"id":"review","agent":"reviewer","task":"review ${find}","needs":["find"]}]}`)
	spec, err := parseWorkflowSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "rev" || len(spec.Steps) != 2 {
		t.Fatalf("bad spec: %+v", spec)
	}
	if spec.Steps[1].Needs[0] != "find" {
		t.Fatalf("deps not parsed: %+v", spec.Steps[1])
	}
	if err := spec.validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

// TestWorkflowCycleDetected: a cyclic DAG is rejected by validation (no daemon
// needed — pure graph check).
func TestWorkflowCycleDetected(t *testing.T) {
	spec := &WorkflowSpec{Name: "loop", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "x", Needs: []string{"b"}},
		{ID: "b", Agent: "worker", Task: "y", Needs: []string{"a"}},
	}}
	if err := spec.validate(); err == nil {
		t.Fatal("cycle should be rejected")
	}
	// Dangling dependency is also rejected.
	bad := &WorkflowSpec{Name: "dangle", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "x", Needs: []string{"ghost"}},
	}}
	if err := bad.validate(); err == nil {
		t.Fatal("dangling dep should be rejected")
	}
}

// TestWorkflowDAGParallelAndInterpolation: a diamond DAG (a → b,c → d) runs
// dependencies before dependents (b,c after a; d after both), independent steps
// in parallel, and threads each step's output into its dependents via
// ${step_id}. We assert containment, which holds regardless of the parallel
// b/c ordering.
func TestWorkflowDAGParallelAndInterpolation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "diamond", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "stepA"},
		{ID: "b", Agent: "worker", Task: "B<${a}>", Needs: []string{"a"}},
		{ID: "c", Agent: "worker", Task: "C<${a}>", Needs: []string{"a"}},
		{ID: "d", Agent: "worker", Task: "D<${b}|${c}>", Needs: []string{"b", "c"}},
	}}

	out, err := d.runWorkflow(parent, parentTask, spec, "", "run-diamond")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 step outputs, got %d: %v", len(out), out)
	}
	// b and c must contain a's output (a ran first, interpolated in).
	if !strings.Contains(out["b"], out["a"]) || !strings.Contains(out["c"], out["a"]) {
		t.Fatalf("deps not interpolated: a=%q b=%q c=%q", out["a"], out["b"], out["c"])
	}
	// d must contain both b's and c's outputs (ran last, after both).
	if !strings.Contains(out["d"], out["b"]) || !strings.Contains(out["d"], out["c"]) {
		t.Fatalf("d did not see b and c: b=%q c=%q d=%q", out["b"], out["c"], out["d"])
	}
}

func TestWorkflowCancellationPropagatesToRunningSubagent(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	reasoner := &cancellationBlockingReasoner{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	d.SetReasoner(reasoner)
	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")
	spec := &WorkflowSpec{Name: "cancel", Steps: []WorkflowStep{{ID: "a", Agent: "worker", Task: "wait"}}}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go d.withTaskParentContext(ctx, parentTask.TaskID, func(context.Context) {
		_, err := d.runWorkflow(parent, parentTask, spec, "", "run-cancel")
		result <- err
	})
	select {
	case <-reasoner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("workflow subagent did not start")
	}
	cancel()
	select {
	case <-reasoner.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("workflow subagent did not observe cancellation")
	}
	close(reasoner.release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("workflow error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workflow did not exit after cancellation")
	}
}

// TestWorkflowResume: a run whose first step is already persisted skips it
// (does not re-invoke the agent) and threads the cached output downstream.
func TestWorkflowResume(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	const runID = "run-resume"
	// Pre-seed step "a" as completed with a marker only a cached load can yield
	// (the reasoner would instead produce "did[stepA]").
	newWFRunStore(d.stateDir).save(runID, map[string]stepResult{
		"a": {Status: "completed", Output: "CACHED_A"},
	})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "resume pipeline")

	spec := &WorkflowSpec{Name: "resumable", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "stepA"},
		{ID: "b", Agent: "worker", Task: "B<${a}>", Needs: []string{"a"}},
	}}
	out, err := d.runWorkflow(parent, parentTask, spec, "", runID)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if out["a"] != "CACHED_A" {
		t.Fatalf("completed step should be skipped and reused, got %q", out["a"])
	}
	if !strings.Contains(out["b"], "CACHED_A") {
		t.Fatalf("downstream should use cached output, got %q", out["b"])
	}
}

// extractTaskLine pulls the "TASK: ..." line out of a subagent prompt, same
// extraction taskEchoReasoner uses.
func extractTaskLine(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "TASK: ") {
			return strings.TrimPrefix(line, "TASK: ")
		}
	}
	return ""
}

// delayedEchoReasoner is taskEchoReasoner plus a controllable block: any task
// containing slowMarker blocks in Think() until proceed is closed. Used to
// prove the streaming scheduler dispatches a fast step's dependent without
// waiting for an unrelated, still-running sibling — the batch/BSP scheduler
// cannot do this by construction (it waits for the whole "level" via
// wg.Wait() before computing the next level).
type delayedEchoReasoner struct {
	slowMarker string
	proceed    chan struct{}
}

func (delayedEchoReasoner) Name() string { return "delayed-echo" }
func (r delayedEchoReasoner) Think(ctx context.Context, prompt string) (string, error) {
	task := extractTaskLine(prompt)
	if strings.Contains(task, r.slowMarker) {
		select {
		case <-r.proceed:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	b, _ := json.Marshal(map[string]string{"tool": "done", "summary": "did[" + task + "]"})
	return string(b), nil
}

// TestWorkflowStreamingDoesNotBarrierOnSlowSibling is the core proof of P1's
// value: "slow" and "fast" are both root steps (no deps between them); "dep"
// depends only on "fast". Under the batch/BSP scheduler both root steps
// would be in the same level, and "dep" (the next level) could not start
// until wg.Wait() returns for the WHOLE level — i.e. not until "slow"
// finishes too. Under streaming, "dep" must start the instant "fast"
// resolves, regardless of "slow" still being blocked.
func TestWorkflowStreamingDoesNotBarrierOnSlowSibling(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	reasoner := delayedEchoReasoner{slowMarker: "SLOW_STEP", proceed: make(chan struct{})}
	d.SetReasoner(reasoner)

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "no-barrier", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "slow", Agent: "worker", Task: "SLOW_STEP"},
		{ID: "fast", Agent: "worker", Task: "FAST_STEP"},
		{ID: "dep", Agent: "worker", Task: "DEP<${fast}>", Needs: []string{"fast"}},
	}}

	type runResult struct {
		out map[string]string
		err error
	}
	resultCh := make(chan runResult, 1)
	go func() {
		out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-no-barrier")
		resultCh <- runResult{out, err}
	}()

	// Poll for "dep" to complete while "slow" is still deliberately blocked —
	// this can only happen if the scheduler dispatched dep without waiting
	// for slow. 2s budget in 10ms steps; fails closed (t.Fatal) on timeout.
	deadline := time.After(2 * time.Second)
	depStarted := false
	for !depStarted {
		select {
		case <-deadline:
			t.Fatal("dep's dependency (fast) never resolved while slow stayed blocked — scheduler appears to be barriering like BSP")
		case <-time.After(10 * time.Millisecond):
			run, _ := newWFRunStore(d.stateDir).load("run-no-barrier")
			if _, ok := run["dep"]; ok {
				depStarted = true
			}
		}
	}
	close(reasoner.proceed)
	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("workflow failed: %v", r.err)
		}
		if !strings.Contains(r.out["dep"], r.out["fast"]) {
			t.Fatalf("dep should have interpolated fast's output: dep=%q fast=%q", r.out["dep"], r.out["fast"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workflow did not complete after releasing the slow step")
	}
}

// TestWorkflowStreamingIsolatesFailureToItsOwnDependents: "root" fails (an
// unknown agent — a real, deterministic failure, not a scripted error);
// "dependent" (needs root) must be skipped, not run; "independent" (no
// relation to root) must still complete normally. This is the direct
// contrast with the batch scheduler's "one failure kills the whole run"
// (workflow.go:245).
func TestWorkflowStreamingIsolatesFailureToItsOwnDependents(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "isolate", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "root", Agent: "does-not-exist", Task: "boom"}, // deterministic "unknown agent" failure
		{ID: "dependent", Agent: "worker", Task: "should be skipped", Needs: []string{"root"}},
		{ID: "independent", Agent: "worker", Task: "unrelated work"},
	}}

	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-isolate")
	if err != nil {
		t.Fatalf("expected the run to succeed overall (isolate is the default), got error: %v", err)
	}
	if _, ok := out["dependent"]; ok {
		t.Fatalf("dependent must be skipped (never ran), got output: %q", out["dependent"])
	}
	if !strings.Contains(out["independent"], "unrelated work") {
		t.Fatalf("independent step must complete normally, got: %q", out["independent"])
	}
}

// TestWorkflowStreamingFailFastAbortsWholeRun: same shape as the isolate
// test, but "root" opts into FailFast — the whole run must abort with an
// error, restoring the old "one failure kills the run" behavior for a step
// that really is on the critical path.
func TestWorkflowStreamingFailFastAbortsWholeRun(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "fail-fast", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "root", Agent: "does-not-exist", Task: "boom", FailFast: true},
		{ID: "dependent", Agent: "worker", Task: "should be skipped", Needs: []string{"root"}},
	}}

	_, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-fail-fast")
	if err == nil {
		t.Fatal("expected FailFast step's failure to abort the whole run")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Fatalf("error should identify the failing step, got: %v", err)
	}
}

// TestWorkflowExecutionModeValidation: an unknown execution_mode is rejected
// up front, same fail-fast-at-declare-time posture as cycle/dangling-dep
// validation.
func TestWorkflowExecutionModeValidation(t *testing.T) {
	spec := &WorkflowSpec{Name: "bad-mode", ExecutionMode: "quantum", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "x"},
	}}
	if err := spec.validate(); err == nil {
		t.Fatal("unknown execution_mode should be rejected")
	}
}

// TestWorkflowStreamingStepLimit: a graph over maxStreamingWorkflowSteps is
// rejected before any step runs.
func TestWorkflowStreamingStepLimit(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	steps := make([]WorkflowStep, maxStreamingWorkflowSteps+1)
	for i := range steps {
		steps[i] = WorkflowStep{ID: fmt.Sprintf("s%d", i), Agent: "worker", Task: "x"}
	}
	spec := &WorkflowSpec{Name: "too-big", ExecutionMode: "streaming", Steps: steps}

	_, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-too-big")
	if err == nil {
		t.Fatal("expected the step-count ceiling to reject this workflow")
	}
}

// TestWorkflowStreamingWideFanOutFanIn is a real scale check, not just a
// small-graph correctness check: 200 independent root steps all feed into
// one join step. This exercises the bounded worker pool under genuine
// contention (200 ready steps competing for defaultStreamingWorkflowConcurrency
// slots) and the join step's in-degree correctly reaching 0 only once all
// 200 have resolved.
func TestWorkflowStreamingWideFanOutFanIn(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	const width = 200
	steps := make([]WorkflowStep, 0, width+1)
	joinNeeds := make([]string, 0, width)
	for i := 0; i < width; i++ {
		id := fmt.Sprintf("leaf%d", i)
		steps = append(steps, WorkflowStep{ID: id, Agent: "worker", Task: id})
		joinNeeds = append(joinNeeds, id)
	}
	steps = append(steps, WorkflowStep{ID: "join", Agent: "worker", Task: "join", Needs: joinNeeds})
	spec := &WorkflowSpec{Name: "wide", ExecutionMode: "streaming", Steps: steps}

	start := time.Now()
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-wide")
	if err != nil {
		t.Fatalf("wide fan-out/fan-in failed: %v", err)
	}
	if len(out) != width+1 {
		t.Fatalf("want %d step outputs, got %d", width+1, len(out))
	}
	if _, ok := out["join"]; !ok {
		t.Fatal("join step never ran")
	}
	t.Logf("200-leaf fan-out/fan-in completed in %s with concurrency=%d", time.Since(start), defaultStreamingWorkflowConcurrency)
}

// scriptedJSONReasoner returns a fixed literal "done" summary for any task
// containing a configured marker (used to script JSON envelopes: condition
// data, structured-input sources, generator spawn_steps), and falls back to
// taskEchoReasoner's plain echo behavior otherwise. If captureMark is set,
// the full prompt for any matching task is also pushed onto captured (for
// tests asserting on exactly what a subagent was told, e.g. resolved
// structured input).
type scriptedJSONReasoner struct {
	byMarker    map[string]string
	captureMark string
	captured    chan string
}

func (r *scriptedJSONReasoner) Name() string { return "scripted-json" }
func (r *scriptedJSONReasoner) Think(_ context.Context, prompt string) (string, error) {
	task := extractTaskLine(prompt)
	if r.captureMark != "" && strings.Contains(task, r.captureMark) {
		select {
		case r.captured <- prompt:
		default:
		}
	}
	for marker, summary := range r.byMarker {
		if strings.Contains(task, marker) {
			b, _ := json.Marshal(map[string]string{"tool": "done", "summary": summary})
			return string(b), nil
		}
	}
	b, _ := json.Marshal(map[string]string{"tool": "done", "summary": "did[" + task + "]"})
	return string(b), nil
}

// TestWorkflowStreamingConditionalEdgeSkipsWhenFalse: two mutually exclusive
// downstream branches each gated by a When condition on the same upstream
// step's JSON output; exactly the branch matching the actual verdict runs.
func TestWorkflowStreamingConditionalEdgeSkipsWhenFalse(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	reasoner := &scriptedJSONReasoner{byMarker: map[string]string{
		"REVIEW_STEP": `{"verdict":"reject","score":2}`,
	}}
	d.SetReasoner(reasoner)

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "conditional", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "review", Agent: "worker", Task: "REVIEW_STEP"},
		{ID: "approve_path", Agent: "worker", Task: "APPROVE", Needs: []string{"review"},
			When: json.RawMessage(`{"==": [{"var":"review.verdict"}, "approve"]}`)},
		{ID: "reject_path", Agent: "worker", Task: "REJECT", Needs: []string{"review"},
			When: json.RawMessage(`{"==": [{"var":"review.verdict"}, "reject"]}`)},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-conditional")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if _, ok := out["approve_path"]; ok {
		t.Fatal("approve_path should have been skipped (condition false)")
	}
	if _, ok := out["reject_path"]; !ok {
		t.Fatal("reject_path should have run (condition true)")
	}
}

// TestWorkflowStreamingStructuredInputResolvesTypedFields verifies a
// downstream step's Input actually resolves into real typed JSON (a number
// stays a number) in the prompt the subagent receives, plus a
// partial-template field interpolates as a string like Task always has.
func TestWorkflowStreamingStructuredInputResolvesTypedFields(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	captured := make(chan string, 1)
	reasoner := &scriptedJSONReasoner{
		byMarker:    map[string]string{"SOURCE_STEP": `{"count":42,"label":"widgets"}`},
		captureMark: "CONSUME_STEP",
		captured:    captured,
	}
	d.SetReasoner(reasoner)

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "structured-input", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "source", Agent: "worker", Task: "SOURCE_STEP"},
		{ID: "consume", Agent: "worker", Task: "CONSUME_STEP", Needs: []string{"source"},
			Input: map[string]string{"n": "${source.count}", "l": "${source.label}", "msg": "count is ${source.count}"}},
	}}
	_, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-structured-input")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}

	select {
	case prompt := <-captured:
		if !strings.Contains(prompt, `"n": 42`) {
			t.Fatalf("expected typed number n=42 in prompt:\n%s", prompt)
		}
		if !strings.Contains(prompt, `"l": "widgets"`) {
			t.Fatalf("expected string field l=widgets in prompt:\n%s", prompt)
		}
		if !strings.Contains(prompt, "count is 42") {
			t.Fatalf("expected interpolated msg in prompt:\n%s", prompt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consume step's prompt was never captured")
	}
}

// TestWorkflowStreamingGeneratorInjectsNewSteps: a generator step's
// spawn_steps envelope actually extends the running graph, and the new step
// runs and contributes to the final output map.
func TestWorkflowStreamingGeneratorInjectsNewSteps(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	reasoner := &scriptedJSONReasoner{byMarker: map[string]string{
		"GEN_STEP": `{"spawn_steps":[{"id":"spawned","agent":"worker","task":"SPAWNED_TASK"}],"rationale":"need one more step"}`,
	}}
	d.SetReasoner(reasoner)

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "generator", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "gen", Agent: "worker", Task: "GEN_STEP", Kind: "generator"},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-generator")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if _, ok := out["spawned"]; !ok {
		t.Fatalf("dynamically spawned step never ran, got outputs: %v", out)
	}
	if !strings.Contains(out["spawned"], "SPAWNED_TASK") {
		t.Fatalf("spawned step's output doesn't reflect its task, got: %q", out["spawned"])
	}
}

// TestWorkflowStreamingGeneratorChainDependsOnSpawnedStep: the generator's
// spawn_steps declares a downstream step that needs the newly-spawned
// sibling — proves cross-references within one injection batch resolve.
func TestWorkflowStreamingGeneratorChainDependsOnSpawnedStep(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	reasoner := &scriptedJSONReasoner{byMarker: map[string]string{
		"GEN_STEP": `{"spawn_steps":[` +
			`{"id":"first","agent":"worker","task":"FIRST_TASK"},` +
			`{"id":"second","agent":"worker","task":"SECOND<${first}>","needs":["first"]}` +
			`]}`,
	}}
	d.SetReasoner(reasoner)

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	spec := &WorkflowSpec{Name: "generator-chain", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "gen", Agent: "worker", Task: "GEN_STEP", Kind: "generator"},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-generator-chain")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if _, ok := out["first"]; !ok {
		t.Fatal("first spawned step never ran")
	}
	if !strings.Contains(out["second"], out["first"]) {
		t.Fatalf("second spawned step should have first's output interpolated: first=%q second=%q", out["first"], out["second"])
	}
}

// The following exercise injectGeneratedSteps' validation directly on a
// manually-constructed coordinator, without a live daemon/reasoner — every
// case here returns an error BEFORE the function ever touches sc.d/sc.parent,
// which is what makes this safe with those fields left as nil zero values.

func TestInjectGeneratedStepsRejectsCollidingID(t *testing.T) {
	sc := &streamCoordinator{byID: map[string]WorkflowStep{"existing": {ID: "existing", Agent: "worker"}}, genDepth: map[string]int{}, totalSteps: 1}
	err := sc.injectGeneratedSteps("gen", `{"spawn_steps":[{"id":"existing","agent":"worker","task":"x"}]}`)
	if err == nil {
		t.Fatal("expected a collision error")
	}
}

func TestInjectGeneratedStepsRejectsUnknownNeeds(t *testing.T) {
	sc := &streamCoordinator{byID: map[string]WorkflowStep{}, genDepth: map[string]int{}, totalSteps: 0}
	err := sc.injectGeneratedSteps("gen", `{"spawn_steps":[{"id":"a","agent":"worker","task":"x","needs":["ghost"]}]}`)
	if err == nil {
		t.Fatal("expected an unknown-needs error")
	}
}

func TestInjectGeneratedStepsRejectsCycleAmongSiblings(t *testing.T) {
	sc := &streamCoordinator{byID: map[string]WorkflowStep{}, genDepth: map[string]int{}, totalSteps: 0}
	err := sc.injectGeneratedSteps("gen", `{"spawn_steps":[{"id":"a","agent":"worker","task":"x","needs":["b"]},{"id":"b","agent":"worker","task":"y","needs":["a"]}]}`)
	if err == nil {
		t.Fatal("expected a cycle error")
	}
}

func TestInjectGeneratedStepsRejectsExceedingMaxDepth(t *testing.T) {
	sc := &streamCoordinator{byID: map[string]WorkflowStep{}, genDepth: map[string]int{"gen": maxGeneratorDepth}, totalSteps: 0}
	err := sc.injectGeneratedSteps("gen", `{"spawn_steps":[{"id":"a","agent":"worker","task":"x"}]}`)
	if err == nil {
		t.Fatal("expected a max-generator-depth error")
	}
}

func TestInjectGeneratedStepsRejectsExceedingStepLimit(t *testing.T) {
	sc := &streamCoordinator{byID: map[string]WorkflowStep{}, genDepth: map[string]int{}, totalSteps: maxStreamingWorkflowSteps}
	err := sc.injectGeneratedSteps("gen", `{"spawn_steps":[{"id":"a","agent":"worker","task":"x"}]}`)
	if err == nil {
		t.Fatal("expected a step-limit error")
	}
}

func TestInjectGeneratedStepsToleratesNonEnvelopeOutput(t *testing.T) {
	sc := &streamCoordinator{byID: map[string]WorkflowStep{}, genDepth: map[string]int{}, totalSteps: 0}
	if err := sc.injectGeneratedSteps("gen", "just some free-text summary, not an envelope"); err != nil {
		t.Fatalf("a generator that produced no envelope should be a no-op, not an error: %v", err)
	}
}

// TestWorkflowStreamingResume: mirrors TestWorkflowResume for the streaming
// scheduler — the streaming path recomputes live in-degree from scratch
// around already-completed steps (genuinely new code, not shared with the
// batch scheduler's resume path), so this exercises that logic directly.
func TestWorkflowStreamingResume(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	const runID = "run-streaming-resume"
	newWFRunStore(d.stateDir).save(runID, map[string]stepResult{
		"a": {Status: "completed", Output: "CACHED_A"},
	})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "resume pipeline")

	spec := &WorkflowSpec{Name: "resumable-streaming", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "stepA"},
		{ID: "b", Agent: "worker", Task: "B<${a}>", Needs: []string{"a"}},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", runID)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if out["a"] != "CACHED_A" {
		t.Fatalf("completed step should be skipped and reused, got %q", out["a"])
	}
	if !strings.Contains(out["b"], "CACHED_A") {
		t.Fatalf("downstream should use cached output, got %q", out["b"])
	}
}
