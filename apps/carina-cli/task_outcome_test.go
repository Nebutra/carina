package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/tui"
)

// TestTaskOutcomeErrorCompletedIsNil pins the happy path: a task that
// reaches "completed" classifies as no error (OutcomeOK via
// classifyExitCode(nil)).
func TestTaskOutcomeErrorCompletedIsNil(t *testing.T) {
	task := map[string]any{"status": "completed", "summary": "done"}
	if err := taskOutcomeError(task); err != nil {
		t.Fatalf("taskOutcomeError(completed) = %v, want nil", err)
	}
}

// TestTaskOutcomeErrorFailedClassifiesAsRuntimeError is the core P1.5(b)
// governance-exit-code fix this closes: before it, `carina run` returned
// nil right after task.submit succeeded and never observed whether the
// task itself failed — a task that ends "failed" must not exit 0.
func TestTaskOutcomeErrorFailedClassifiesAsRuntimeError(t *testing.T) {
	task := map[string]any{"status": "failed", "summary": "model error: rate limited"}
	err := taskOutcomeError(task)
	if err == nil {
		t.Fatal("taskOutcomeError(failed) = nil, want a non-nil error")
	}
	if got := classifyExitCode(err); got != tui.OutcomeRuntimeError {
		t.Fatalf("classifyExitCode(taskOutcomeError(failed)) = %v, want OutcomeRuntimeError", got)
	}
}

// TestTaskOutcomeErrorDegradedClassifiesAsDegradedPartial pins the
// degrade-not-failure distinction (exit 6): a task that could not reach
// "done" but made partial, rollbackable progress must not collapse into
// the same exit code as a hard failure.
func TestTaskOutcomeErrorDegradedClassifiesAsDegradedPartial(t *testing.T) {
	task := map[string]any{"status": "degraded", "summary": "reached max turns without done"}
	err := taskOutcomeError(task)
	if err == nil {
		t.Fatal("taskOutcomeError(degraded) = nil, want a non-nil error")
	}
	if got := classifyExitCode(err); got != tui.OutcomeDegradedPartial {
		t.Fatalf("classifyExitCode(taskOutcomeError(degraded)) = %v, want OutcomeDegradedPartial", got)
	}
}

// TestTaskOutcomeErrorSummaryCarriesPolicyDeniedPrefix proves a task whose
// terminal summary embeds the daemon's own "DENIED by policy: ..." verdict
// text (as agentRun/dispatchAction emit it into the transcript, which can
// surface into a degrade/fail summary) still classifies distinctly via
// classifyExitCode's existing policyDeniedPrefix match — taskOutcomeError
// must pass the summary through unmodified, not paraphrase it away.
func TestTaskOutcomeErrorSummaryCarriesPolicyDeniedPrefix(t *testing.T) {
	task := map[string]any{"status": "degraded", "summary": "DENIED by policy: destructive command blocked by rule cmd.rm-rf"}
	err := taskOutcomeError(task)
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	if got := classifyExitCode(err); got != tui.OutcomePolicyDenied {
		t.Fatalf("classifyExitCode = %v, want OutcomePolicyDenied (summary text must be preserved verbatim)", got)
	}
}

// TestTaskOutcomeErrorWaitingApprovalIsDistinctSentinel pins the
// non-terminal "task is paused for a human decision" case: pollTaskUntilTerminal
// must not treat waiting_approval as done, but if a caller ever passes it to
// taskOutcomeError directly it must not silently classify as OK either.
func TestTaskOutcomeErrorWaitingApprovalIsDistinctSentinel(t *testing.T) {
	task := map[string]any{"status": "waiting_approval", "task_id": "t1"}
	err := taskOutcomeError(task)
	if err == nil {
		t.Fatal("taskOutcomeError(waiting_approval) = nil, want a non-nil sentinel (never silently OK)")
	}
	if !errors.Is(err, errTaskWaitingApproval) {
		t.Fatalf("taskOutcomeError(waiting_approval) = %v, want it to wrap errTaskWaitingApproval", err)
	}
}

// TestIsTerminalTaskStatus pins the terminal/non-terminal status split
// pollTaskUntilTerminal relies on.
func TestIsTerminalTaskStatus(t *testing.T) {
	terminal := []string{"completed", "degraded", "failed", "cancelled"}
	for _, s := range terminal {
		if !isTerminalTaskStatus(s) {
			t.Errorf("isTerminalTaskStatus(%q) = false, want true", s)
		}
	}
	nonTerminal := []string{"queued", "running", "paused", "waiting_approval"}
	for _, s := range nonTerminal {
		if isTerminalTaskStatus(s) {
			t.Errorf("isTerminalTaskStatus(%q) = true, want false", s)
		}
	}
}

// TestHasFlagAndDropFlagBackgroundOptOut pins the exact args-parsing
// interplay run()'s "run"/"ask" case relies on: --background must be
// detected via hasFlag BEFORE dropFlag strips it, so the CLI can still tell
// whether to skip runWaitForTask after parseRunArgs consumes the remaining
// flags/prompt.
func TestHasFlagAndDropFlagBackgroundOptOut(t *testing.T) {
	args := []string{"--model", "openai/gpt-5", "--background", "ship it"}
	if !hasFlag(args, "--background") {
		t.Fatal("hasFlag should detect --background before it is dropped")
	}
	stripped := dropFlag(args, "--background")
	if hasFlag(stripped, "--background") {
		t.Fatal("dropFlag should remove --background")
	}
	prompt, model, _, err := parseRunArgs(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "ship it" || model != "openai/gpt-5" {
		t.Fatalf("prompt=%q model=%q, want ship it / openai/gpt-5", prompt, model)
	}
}

// fakeTaskStatusPoller replays a fixed sequence of task.status responses,
// one per Call, so runWaitForTask's polling loop is testable without a
// live daemon connection or real time.Sleep.
type fakeTaskStatusPoller struct {
	responses []map[string]any
	calls     int
}

func (f *fakeTaskStatusPoller) Call(method string, params any, result any) error {
	if method != "task.status" {
		return fmt.Errorf("unexpected method %q", method)
	}
	resp := f.responses[len(f.responses)-1]
	if f.calls < len(f.responses) {
		resp = f.responses[f.calls]
	}
	f.calls++
	ptr, ok := result.(*map[string]any)
	if !ok {
		return fmt.Errorf("unexpected result type %T", result)
	}
	*ptr = resp
	return nil
}

// TestRunWaitForTaskReturnsImmediatelyOnFirstTerminalPoll proves the
// P1.5(b) fix end to end at the polling layer: a task already terminal on
// the very first task.status poll must classify without looping.
func TestRunWaitForTaskReturnsImmediatelyOnFirstTerminalPoll(t *testing.T) {
	poller := &fakeTaskStatusPoller{responses: []map[string]any{
		{"status": "completed", "summary": "done"},
	}}
	var slept int
	err := runWaitForTask(poller, "t1", time.Minute, func(time.Duration) { slept++ })
	if err != nil {
		t.Fatalf("runWaitForTask = %v, want nil for a completed task", err)
	}
	if poller.calls != 1 {
		t.Fatalf("expected exactly 1 task.status call, got %d", poller.calls)
	}
	if slept != 0 {
		t.Fatalf("expected no sleep once the first poll is already terminal, got %d sleeps", slept)
	}
}

// TestRunWaitForTaskPollsThroughRunningToFailed proves the polling loop
// advances through non-terminal statuses and returns the classified error
// once the task reaches "failed" — the exact gap the finding describes:
// `carina run` must observe this, not return nil after task.submit.
func TestRunWaitForTaskPollsThroughRunningToFailed(t *testing.T) {
	poller := &fakeTaskStatusPoller{responses: []map[string]any{
		{"status": "queued"},
		{"status": "running"},
		{"status": "failed", "summary": "model error: rate limited"},
	}}
	var slept int
	err := runWaitForTask(poller, "t1", time.Minute, func(time.Duration) { slept++ })
	if err == nil {
		t.Fatal("runWaitForTask = nil, want a non-nil error for a task that ends failed")
	}
	if got := classifyExitCode(err); got != tui.OutcomeRuntimeError {
		t.Fatalf("classifyExitCode(runWaitForTask result) = %v, want OutcomeRuntimeError", got)
	}
	if poller.calls != 3 {
		t.Fatalf("expected 3 task.status calls (queued, running, failed), got %d", poller.calls)
	}
	if slept != 2 {
		t.Fatalf("expected 2 sleeps between the 3 polls, got %d", slept)
	}
}

// TestRunWaitForTaskGivesUpAfterTimeout proves the poll loop is bounded: a
// task stuck in "running" forever must not hang the CLI process
// indefinitely — it must return a non-nil error once the timeout elapses.
func TestRunWaitForTaskGivesUpAfterTimeout(t *testing.T) {
	poller := &fakeTaskStatusPoller{responses: []map[string]any{
		{"status": "running"},
	}}
	var slept int
	// runWaitForTask uses time.Now() internally; drive it to exceed a tiny
	// timeout using a real (but tiny) duration so the test stays fast
	// without needing to inject a clock.
	err := runWaitForTask(poller, "t1", 10*time.Millisecond, func(d time.Duration) {
		slept++
		time.Sleep(d)
	})
	if err == nil {
		t.Fatal("runWaitForTask = nil, want a non-nil error once the timeout elapses on a non-terminal task")
	}
	if slept == 0 {
		t.Fatal("expected at least one sleep before giving up")
	}
}
