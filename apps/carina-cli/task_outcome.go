package main

import (
	"errors"
	"fmt"
	"time"
)

// executionDegradedError carries a "degraded" execution's summary so classifyExitCode
// can map it to outcome.OutcomeDegradedPartial (exit 6) — distinct from a hard
// "failed" execution (exit 1): a degrade means partial, rollbackable progress
// was made, not a bare failure. Checked in classifyExitCode AFTER the
// policyDeniedPrefix/userDeniedPrefix string matches, so a degrade whose
// summary happens to embed the daemon's own governance-denial text still
// classifies as the more specific policy/user-denied outcome.
type executionDegradedError struct {
	summary string
}

func (e *executionDegradedError) Error() string {
	return "execution degraded: " + e.summary
}

// errExecutionWaitingApproval is the sentinel executionOutcomeError wraps when a
// execution's terminal poll observed status "waiting_approval" — a execution paused
// for a human decision, not a governance verdict in itself. runWaitForExecution
// treats this as non-terminal and keeps polling; executionOutcomeError still
// never returns nil for it, so a caller that (mis)uses it directly cannot
// accidentally classify a pending-approval execution as success.
var errExecutionWaitingApproval = errors.New("execution is waiting on an approval decision")

// terminalExecutionStatuses are the execution.status values go/scheduler.ExecutionRun reaches
// and never leaves (go/daemon/agent.go's finish/degrade/fail call sites):
// completed, degraded, failed, cancelled.
var terminalExecutionStatuses = map[string]bool{
	"completed": true,
	"degraded":  true,
	"failed":    true,
	"cancelled": true,
}

// isTerminalExecutionStatus reports whether status is one runWaitForExecution should
// stop polling on.
func isTerminalExecutionStatus(status string) bool {
	return terminalExecutionStatuses[status]
}

// executionOutcomeError converts a execution.status response into the terminal error
// `run`/`ask` returns to classifyExitCode (P1.5(b)'s missing half): before
// this, `carina run` returned nil the instant execution.start's RPC round trip
// succeeded, never observing whether the submitted execution itself failed,
// degraded, or was denied — so a genuinely policy-denied or failed run
// exited 0 exactly like a clean success. nil only for "completed"; every
// other status (including a terminal fail/degrade whose summary carries
// the daemon's own "DENIED by policy: .../denied by user: ..." verbatim
// text, still matched by classifyExitCode's existing prefix checks) or the
// non-terminal waiting_approval sentinel returns a non-nil error.
func executionOutcomeError(execution map[string]any) error {
	status, _ := execution["status"].(string)
	summary, _ := execution["summary"].(string)
	switch status {
	case "completed":
		return nil
	case "waiting_approval":
		runID, _ := execution["run_id"].(string)
		return fmt.Errorf("execution %s is waiting on an approval — run `carina approve`/`carina deny` to resolve it: %w", runID, errExecutionWaitingApproval)
	case "degraded":
		if summary == "" {
			summary = "reached max turns without done"
		}
		return &executionDegradedError{summary: summary}
	case "failed", "cancelled":
		if summary == "" {
			summary = status
		}
		return fmt.Errorf("execution %s: %s", status, summary)
	default:
		// queued/running/paused observed here means the poll loop gave up
		// before the execution reached a terminal state (timeout) — a runtime
		// error distinct from any of the above, not a silent OK.
		return fmt.Errorf("execution did not reach a terminal state (status=%s)", status)
	}
}

// executionStatusPoller is the minimal RPC surface runWaitForExecution needs —
// satisfied by *rpcClient, narrowed so this file's logic is testable
// without a live daemon connection.
type executionStatusPoller interface {
	Call(method string, params any, result any) error
}

// runWaitForExecutionDefaultTimeout bounds how long the foreground `carina run`/
// `ask` path polls execution.status before giving up: generous enough for a real
// agent turn, but never an unbounded hang — a CLI invocation must return
// control to its caller (a script, a CI job) within a governance-observable
// bound.
const runWaitForExecutionDefaultTimeout = 10 * time.Minute

// runWaitForExecutionPollInterval is the delay between execution.status polls.
const runWaitForExecutionPollInterval = 500 * time.Millisecond

// runWaitForExecution polls execution.status until the execution reaches a terminal
// status (isTerminalExecutionStatus) or the timeout elapses, then returns
// executionOutcomeError's classification of the final observed state. This is
// what makes foreground `carina run`/`ask` (P1.5(b)'s "a foreground carina
// run ... exits with a governance-distinct code") actually observe the
// execution's real outcome instead of returning nil the instant execution.start's
// RPC round trip completes.
func runWaitForExecution(c executionStatusPoller, runID string, timeout time.Duration, sleep func(time.Duration)) error {
	if timeout <= 0 {
		timeout = runWaitForExecutionDefaultTimeout
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	deadline := time.Now().Add(timeout)
	var execution map[string]any
	for {
		execution = nil
		if err := c.Call("execution.status", map[string]any{"run_id": runID}, &execution); err != nil {
			return err
		}
		status, _ := execution["status"].(string)
		// waiting_approval keeps polling past the deadline check below like
		// any other non-terminal status — a human resolving it via `carina
		// approve`/`deny` in another terminal is expected, ordinary
		// behavior, not a stall to give up on early. It still is not
		// terminal, so isTerminalExecutionStatus correctly excludes it here.
		if isTerminalExecutionStatus(status) {
			return executionOutcomeError(execution)
		}
		if time.Now().After(deadline) {
			return executionOutcomeError(execution)
		}
		sleep(runWaitForExecutionPollInterval)
	}
}
