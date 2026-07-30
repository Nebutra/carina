package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/outcome"
)

// TestExecutionOutcomeErrorCompletedIsNil pins the happy path: a execution that
// reaches "completed" classifies as no error (OutcomeOK via
// classifyExitCode(nil)).
func TestExecutionOutcomeErrorCompletedIsNil(t *testing.T) {
	execution := map[string]any{"status": "completed", "summary": "done"}
	if err := executionOutcomeError(execution); err != nil {
		t.Fatalf("executionOutcomeError(completed) = %v, want nil", err)
	}
}

// TestExecutionOutcomeErrorFailedClassifiesAsRuntimeError is the core P1.5(b)
// governance-exit-code fix this closes: before it, `carina run` returned
// nil right after execution.start succeeded and never observed whether the
// execution itself failed — a execution that ends "failed" must not exit 0.
func TestExecutionOutcomeErrorFailedClassifiesAsRuntimeError(t *testing.T) {
	execution := map[string]any{"status": "failed", "summary": "model error: rate limited"}
	err := executionOutcomeError(execution)
	if err == nil {
		t.Fatal("executionOutcomeError(failed) = nil, want a non-nil error")
	}
	if got := classifyExitCode(err); got != outcome.OutcomeRuntimeError {
		t.Fatalf("classifyExitCode(executionOutcomeError(failed)) = %v, want OutcomeRuntimeError", got)
	}
}

// TestExecutionOutcomeErrorDegradedClassifiesAsDegradedPartial pins the
// degrade-not-failure distinction (exit 6): a execution that could not reach
// "done" but made partial, rollbackable progress must not collapse into
// the same exit code as a hard failure.
func TestExecutionOutcomeErrorDegradedClassifiesAsDegradedPartial(t *testing.T) {
	execution := map[string]any{"status": "degraded", "summary": "reached max turns without done"}
	err := executionOutcomeError(execution)
	if err == nil {
		t.Fatal("executionOutcomeError(degraded) = nil, want a non-nil error")
	}
	if got := classifyExitCode(err); got != outcome.OutcomeDegradedPartial {
		t.Fatalf("classifyExitCode(executionOutcomeError(degraded)) = %v, want OutcomeDegradedPartial", got)
	}
}

// TestExecutionOutcomeErrorSummaryCarriesPolicyDeniedPrefix proves a execution whose
// terminal summary embeds the daemon's own "DENIED by policy: ..." verdict
// text (as agentRun/dispatchAction emit it into the transcript, which can
// surface into a degrade/fail summary) still classifies distinctly via
// classifyExitCode's existing policyDeniedPrefix match — executionOutcomeError
// must pass the summary through unmodified, not paraphrase it away.
func TestExecutionOutcomeErrorSummaryCarriesPolicyDeniedPrefix(t *testing.T) {
	execution := map[string]any{"status": "degraded", "summary": "DENIED by policy: destructive command blocked by rule cmd.rm-rf"}
	err := executionOutcomeError(execution)
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	if got := classifyExitCode(err); got != outcome.OutcomePolicyDenied {
		t.Fatalf("classifyExitCode = %v, want OutcomePolicyDenied (summary text must be preserved verbatim)", got)
	}
}

// TestExecutionOutcomeErrorWaitingApprovalIsDistinctSentinel pins the
// non-terminal "execution is paused for a human decision" case: pollExecutionUntilTerminal
// must not treat waiting_approval as done, but if a caller ever passes it to
// executionOutcomeError directly it must not silently classify as OK either.
func TestExecutionOutcomeErrorWaitingApprovalIsDistinctSentinel(t *testing.T) {
	execution := map[string]any{"status": "waiting_approval", "run_id": "t1"}
	err := executionOutcomeError(execution)
	if err == nil {
		t.Fatal("executionOutcomeError(waiting_approval) = nil, want a non-nil sentinel (never silently OK)")
	}
	if !errors.Is(err, errExecutionWaitingApproval) {
		t.Fatalf("executionOutcomeError(waiting_approval) = %v, want it to wrap errExecutionWaitingApproval", err)
	}
}

// TestIsTerminalExecutionStatus pins the terminal/non-terminal status split
// pollExecutionUntilTerminal relies on.
func TestIsTerminalExecutionStatus(t *testing.T) {
	terminal := []string{"completed", "degraded", "failed", "cancelled"}
	for _, s := range terminal {
		if !isTerminalExecutionStatus(s) {
			t.Errorf("isTerminalExecutionStatus(%q) = false, want true", s)
		}
	}
	nonTerminal := []string{"queued", "running", "paused", "waiting_approval"}
	for _, s := range nonTerminal {
		if isTerminalExecutionStatus(s) {
			t.Errorf("isTerminalExecutionStatus(%q) = true, want false", s)
		}
	}
}

// TestHasFlagAndDropFlagBackgroundOptOut pins the exact args-parsing
// interplay run()'s "run"/"ask" case relies on: --background must be
// detected via hasFlag BEFORE dropFlag strips it, so the CLI can still tell
// whether to skip runWaitForExecution after parseRunArgs consumes the remaining
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

// fakeExecutionStatusPoller replays a fixed sequence of execution.status responses,
// one per Call, so runWaitForExecution's polling loop is testable without a
// live daemon connection or real time.Sleep.
type fakeExecutionStatusPoller struct {
	responses []map[string]any
	calls     int
}

func (f *fakeExecutionStatusPoller) Call(method string, params any, result any) error {
	if method != "execution.status" {
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

// TestRunWaitForExecutionReturnsImmediatelyOnFirstTerminalPoll proves the
// P1.5(b) fix end to end at the polling layer: a execution already terminal on
// the very first execution.status poll must classify without looping.
func TestRunWaitForExecutionReturnsImmediatelyOnFirstTerminalPoll(t *testing.T) {
	poller := &fakeExecutionStatusPoller{responses: []map[string]any{
		{"status": "completed", "summary": "done"},
	}}
	var slept int
	err := runWaitForExecution(poller, "t1", time.Minute, func(time.Duration) { slept++ })
	if err != nil {
		t.Fatalf("runWaitForExecution = %v, want nil for a completed execution", err)
	}
	if poller.calls != 1 {
		t.Fatalf("expected exactly 1 execution.status call, got %d", poller.calls)
	}
	if slept != 0 {
		t.Fatalf("expected no sleep once the first poll is already terminal, got %d sleeps", slept)
	}
}

// TestRunWaitForExecutionPollsThroughRunningToFailed proves the polling loop
// advances through non-terminal statuses and returns the classified error
// once the execution reaches "failed" — the exact gap the finding describes:
// `carina run` must observe this, not return nil after execution.start.
func TestRunWaitForExecutionPollsThroughRunningToFailed(t *testing.T) {
	poller := &fakeExecutionStatusPoller{responses: []map[string]any{
		{"status": "queued"},
		{"status": "running"},
		{"status": "failed", "summary": "model error: rate limited"},
	}}
	var slept int
	err := runWaitForExecution(poller, "t1", time.Minute, func(time.Duration) { slept++ })
	if err == nil {
		t.Fatal("runWaitForExecution = nil, want a non-nil error for a execution that ends failed")
	}
	if got := classifyExitCode(err); got != outcome.OutcomeRuntimeError {
		t.Fatalf("classifyExitCode(runWaitForExecution result) = %v, want OutcomeRuntimeError", got)
	}
	if poller.calls != 3 {
		t.Fatalf("expected 3 execution.status calls (queued, running, failed), got %d", poller.calls)
	}
	if slept != 2 {
		t.Fatalf("expected 2 sleeps between the 3 polls, got %d", slept)
	}
}

// TestRunWaitForExecutionGivesUpAfterTimeout proves the poll loop is bounded: a
// execution stuck in "running" forever must not hang the CLI process
// indefinitely — it must return a non-nil error once the timeout elapses.
func TestRunWaitForExecutionGivesUpAfterTimeout(t *testing.T) {
	poller := &fakeExecutionStatusPoller{responses: []map[string]any{
		{"status": "running"},
	}}
	var slept int
	// runWaitForExecution uses time.Now() internally; drive it to exceed a tiny
	// timeout using a real (but tiny) duration so the test stays fast
	// without needing to inject a clock.
	err := runWaitForExecution(poller, "t1", 10*time.Millisecond, func(d time.Duration) {
		slept++
		time.Sleep(d)
	})
	if err == nil {
		t.Fatal("runWaitForExecution = nil, want a non-nil error once the timeout elapses on a non-terminal execution")
	}
	if slept == 0 {
		t.Fatal("expected at least one sleep before giving up")
	}
}
