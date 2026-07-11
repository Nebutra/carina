package daemon

import (
	"strings"
	"testing"
)

func TestSessionTotalTokensSumsAcrossTasksForSession(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")

	t1 := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "first")
	d.sched.AddTokens(t1.TaskID, 100)
	t2 := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "second")
	d.sched.AddTokens(t2.TaskID, 40)

	other, _ := d.store.CreateSession(ws, "safe-edit")
	t3 := d.sched.Submit(other.SessionID, other.WorkspaceID, "unrelated")
	d.sched.AddTokens(t3.TaskID, 999)

	if got := d.sessionTotalTokens(sess.SessionID); got != 140 {
		t.Fatalf("sessionTotalTokens = %d, want 140 (100+40, excluding the other session's 999)", got)
	}
}

// lastRollup finds the most recent workflow_progress_rollup payload observed
// by sub — the coordinator emits one after every step resolution, so the
// last one reflects final run state.
func lastRollup(t *testing.T, sub *fakeEventSub) map[string]any {
	t.Helper()
	sub.mu.Lock()
	defer sub.mu.Unlock()
	for i := len(sub.events) - 1; i >= 0; i-- {
		payload, ok := sub.events[i]["payload"].(map[string]any)
		if !ok {
			continue
		}
		if payload["status"] == "workflow_progress_rollup" {
			return payload
		}
	}
	t.Fatal("no workflow_progress_rollup event observed")
	return nil
}

func TestWorkflowStreamingRollupTracksFinalProgressCounts(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	sub := newFakeEventSub("rollup-progress")
	d.events.Subscribe(parent.SessionID, sub)

	spec := &WorkflowSpec{Name: "rollup", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "a", Agent: "worker", Task: "A"},
		{ID: "b", Agent: "worker", Task: "B"},
		{ID: "c", Agent: "worker", Task: "C", Needs: []string{"a", "b"}},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-rollup")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected all 3 steps to complete, got %v", out)
	}

	rollup := lastRollup(t, sub)
	if rollup["total"] != 3 || rollup["completed"] != 3 || rollup["running"] != 0 ||
		rollup["failed"] != 0 || rollup["skipped"] != 0 || rollup["queued"] != 0 {
		t.Fatalf("final rollup should show 3/3 completed and nothing else in flight, got: %+v", rollup)
	}
}

func TestWorkflowStreamingRollupIncludesBudgetFieldsWhenTokenBudgetSet(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	sub := newFakeEventSub("rollup-budget")
	d.events.Subscribe(parent.SessionID, sub)

	spec := &WorkflowSpec{Name: "rollup-budget", ExecutionMode: "streaming", TokenBudget: 100000, Steps: []WorkflowStep{
		{ID: "solo", Agent: "worker", Task: "SOLO"},
	}}
	if _, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-rollup-budget"); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}

	rollup := lastRollup(t, sub)
	spent, _ := rollup["budget_spent"].(int)
	limit, _ := rollup["budget_limit"].(int)
	remaining, _ := rollup["budget_remaining"].(int)
	if limit != 100000 {
		t.Fatalf("budget_limit should echo the spec's TokenBudget, got %+v", rollup)
	}
	if spent <= 0 {
		t.Fatalf("a real subagent turn must have burned a positive token count, got spent=%d", spent)
	}
	if remaining != limit-spent {
		t.Fatalf("budget_remaining should be limit-spent exactly, got remaining=%d limit=%d spent=%d", remaining, limit, spent)
	}
}

func TestWorkflowStreamingBudgetExhaustionSkipsRemainingStepsWithoutAbortingRun(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	sub := newFakeEventSub("rollup-exhaust")
	d.events.Subscribe(parent.SessionID, sub)

	// TokenBudget: 1 guarantees exhaustion the instant "first" completes (any
	// real subagent turn burns far more than 1 token), and "second" needs
	// "first" — so its readiness check (which happens synchronously inside
	// the SAME handleResult call that just added first's cost to
	// budgetSpent) deterministically sees the budget already exhausted,
	// with no race against a concurrently-dispatched sibling.
	spec := &WorkflowSpec{Name: "budget-exhaust", ExecutionMode: "streaming", TokenBudget: 1, Steps: []WorkflowStep{
		{ID: "first", Agent: "worker", Task: "FIRST"},
		{ID: "second", Agent: "worker", Task: "SECOND", Needs: []string{"first"}},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-budget-exhaust")
	if err != nil {
		t.Fatalf("budget exhaustion must isolate (skip remaining steps), not abort the whole run: %v", err)
	}
	if _, ok := out["first"]; !ok {
		t.Fatal("the step that established the budget spend should still have completed")
	}
	if _, ok := out["second"]; ok {
		t.Fatal("second should have been skipped once the token budget was exhausted")
	}

	sub.mu.Lock()
	defer sub.mu.Unlock()
	found := false
	for _, evt := range sub.events {
		payload, ok := evt["payload"].(map[string]any)
		if !ok || payload["status"] != "workflow_step_skipped" {
			continue
		}
		if payload["step"] == "second" {
			reason, _ := payload["reason"].(string)
			if !strings.Contains(reason, "token budget exhausted") {
				t.Fatalf("expected the skip reason to mention token budget exhaustion, got %q", reason)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected an audited workflow_step_skipped event for the budget-exhausted step")
	}
}

// TestWorkflowStreamingRollupIncludesChannelActivityStats proves swarm
// channel activity (P3) is actually visible in the P5 aggregate rollup —
// previously a real observability gap: an operator watching only the
// rollup stream had no way to tell a swarm channel was even in use.
func TestWorkflowStreamingRollupIncludesChannelActivityStats(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeChannelWorkerAgent(t, ws)
	d.SetReasoner(&swarmChannelTestReasoner{publishMarker: "PUBLISH_STEP"})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	sub := newFakeEventSub("rollup-channel-stats")
	d.events.Subscribe(parent.SessionID, sub)

	spec := &WorkflowSpec{Name: "rollup-channel", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "publisher", Agent: "channel-worker", Task: "PUBLISH_STEP"},
	}}
	if _, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-rollup-channel"); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}

	rollup := lastRollup(t, sub)
	published, _ := rollup["channel_messages_published"].(int)
	if published < 1 {
		t.Fatalf("expected the rollup to reflect at least 1 published swarm message, got: %+v", rollup)
	}
	if _, hasEvicted := rollup["channel_messages_evicted"]; hasEvicted {
		t.Fatalf("no eviction happened in this run, channel_messages_evicted should be absent, got: %+v", rollup)
	}
}

func TestWorkflowStreamingNoBudgetMeansUnlimited(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	writeWorkerAgent(t, ws)
	d.SetReasoner(taskEchoReasoner{})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "run pipeline")

	// TokenBudget left at its zero value (unset) — must never skip anything
	// on budget grounds, matching the "0 means unlimited" convention
	// d.maxTaskTokens already uses.
	spec := &WorkflowSpec{Name: "budget-unset", ExecutionMode: "streaming", Steps: []WorkflowStep{
		{ID: "first", Agent: "worker", Task: "FIRST"},
		{ID: "second", Agent: "worker", Task: "SECOND", Needs: []string{"first"}},
	}}
	out, err := d.runWorkflowStreaming(parent, parentTask, spec, "", "run-budget-unset")
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected both steps to run with no TokenBudget set, got %v", out)
	}
}
