package main

import (
	"errors"
	"fmt"
	"time"
)

// taskDegradedError carries a "degraded" task's summary so classifyExitCode
// can map it to tui.OutcomeDegradedPartial (exit 6) — distinct from a hard
// "failed" task (exit 1): a degrade means partial, rollbackable progress
// was made, not a bare failure. Checked in classifyExitCode AFTER the
// policyDeniedPrefix/userDeniedPrefix string matches, so a degrade whose
// summary happens to embed the daemon's own governance-denial text still
// classifies as the more specific policy/user-denied outcome.
type taskDegradedError struct {
	summary string
}

func (e *taskDegradedError) Error() string {
	return "task degraded: " + e.summary
}

// errTaskWaitingApproval is the sentinel taskOutcomeError wraps when a
// task's terminal poll observed status "waiting_approval" — a task paused
// for a human decision, not a governance verdict in itself. runWaitForTask
// treats this as non-terminal and keeps polling; taskOutcomeError still
// never returns nil for it, so a caller that (mis)uses it directly cannot
// accidentally classify a pending-approval task as success.
var errTaskWaitingApproval = errors.New("task is waiting on an approval decision")

// terminalTaskStatuses are the task.status values go/scheduler.Task reaches
// and never leaves (go/daemon/agent.go's finish/degrade/fail call sites):
// completed, degraded, failed, cancelled.
var terminalTaskStatuses = map[string]bool{
	"completed": true,
	"degraded":  true,
	"failed":    true,
	"cancelled": true,
}

// isTerminalTaskStatus reports whether status is one runWaitForTask should
// stop polling on.
func isTerminalTaskStatus(status string) bool {
	return terminalTaskStatuses[status]
}

// taskOutcomeError converts a task.status response into the terminal error
// `run`/`ask` returns to classifyExitCode (P1.5(b)'s missing half): before
// this, `carina run` returned nil the instant task.submit's RPC round trip
// succeeded, never observing whether the submitted task itself failed,
// degraded, or was denied — so a genuinely policy-denied or failed run
// exited 0 exactly like a clean success. nil only for "completed"; every
// other status (including a terminal fail/degrade whose summary carries
// the daemon's own "DENIED by policy: .../denied by user: ..." verbatim
// text, still matched by classifyExitCode's existing prefix checks) or the
// non-terminal waiting_approval sentinel returns a non-nil error.
func taskOutcomeError(task map[string]any) error {
	status, _ := task["status"].(string)
	summary, _ := task["summary"].(string)
	switch status {
	case "completed":
		return nil
	case "waiting_approval":
		taskID, _ := task["task_id"].(string)
		return fmt.Errorf("task %s is waiting on an approval — run `carina approve`/`carina deny` to resolve it: %w", taskID, errTaskWaitingApproval)
	case "degraded":
		if summary == "" {
			summary = "reached max turns without done"
		}
		return &taskDegradedError{summary: summary}
	case "failed", "cancelled":
		if summary == "" {
			summary = status
		}
		return fmt.Errorf("task %s: %s", status, summary)
	default:
		// queued/running/paused observed here means the poll loop gave up
		// before the task reached a terminal state (timeout) — a runtime
		// error distinct from any of the above, not a silent OK.
		return fmt.Errorf("task did not reach a terminal state (status=%s)", status)
	}
}

// taskStatusPoller is the minimal RPC surface runWaitForTask needs —
// satisfied by *rpcClient, narrowed so this file's logic is testable
// without a live daemon connection.
type taskStatusPoller interface {
	Call(method string, params any, result any) error
}

// runWaitForTaskDefaultTimeout bounds how long the foreground `carina run`/
// `ask` path polls task.status before giving up: generous enough for a real
// agent turn, but never an unbounded hang — a CLI invocation must return
// control to its caller (a script, a CI job) within a governance-observable
// bound.
const runWaitForTaskDefaultTimeout = 10 * time.Minute

// runWaitForTaskPollInterval is the delay between task.status polls.
const runWaitForTaskPollInterval = 500 * time.Millisecond

// runWaitForTask polls task.status until the task reaches a terminal
// status (isTerminalTaskStatus) or the timeout elapses, then returns
// taskOutcomeError's classification of the final observed state. This is
// what makes foreground `carina run`/`ask` (P1.5(b)'s "a foreground carina
// run ... exits with a governance-distinct code") actually observe the
// task's real outcome instead of returning nil the instant task.submit's
// RPC round trip completes.
func runWaitForTask(c taskStatusPoller, taskID string, timeout time.Duration, sleep func(time.Duration)) error {
	if timeout <= 0 {
		timeout = runWaitForTaskDefaultTimeout
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	deadline := time.Now().Add(timeout)
	var task map[string]any
	for {
		task = nil
		if err := c.Call("task.status", map[string]any{"task_id": taskID}, &task); err != nil {
			return err
		}
		status, _ := task["status"].(string)
		// waiting_approval keeps polling past the deadline check below like
		// any other non-terminal status — a human resolving it via `carina
		// approve`/`deny` in another terminal is expected, ordinary
		// behavior, not a stall to give up on early. It still is not
		// terminal, so isTerminalTaskStatus correctly excludes it here.
		if isTerminalTaskStatus(status) {
			return taskOutcomeError(task)
		}
		if time.Now().After(deadline) {
			return taskOutcomeError(task)
		}
		sleep(runWaitForTaskPollInterval)
	}
}
