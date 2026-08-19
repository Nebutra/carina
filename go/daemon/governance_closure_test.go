package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/scheduler"
)

func TestMemoryWriteAuditFailureLeavesAuthorityUnchanged(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	scope := memoryScopeFromSession(sess)
	before, _ := d.memory.list(scope, memoryTargetMemory)
	_ = d.Kernel().Close()
	_, err := d.applyMemoryWrite(sess, "", memoryWriteRequest{Action: "add", Target: "memory", Content: "must not persist"}, &kernel.Decision{Decision: "allowed"}, scope, memoryWriteSummary{Target: "memory", Action: "add", ScopeID: scope.WorkspaceHash, Resource: "test", ContentSHA256: "hash", OperationCount: 1})
	if err == nil || !strings.Contains(err.Error(), "audit WAL") {
		t.Fatalf("expected strict audit failure, got %v", err)
	}
	after, _ := d.memory.list(scope, memoryTargetMemory)
	if strings.Join(before.Entries, "\n") != strings.Join(after.Entries, "\n") {
		t.Fatalf("memory changed without audit: before=%v after=%v", before.Entries, after.Entries)
	}
}

func TestAutoGoalCompletesFromVerifiedTerminalTask(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"done"}`}})
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	if _, err := d.handleGoalSet(mustJSON(t, map[string]any{"session_id": sess.SessionID, "objective": "finish", "auto_continue": true})); err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleGoalContinue(mustJSON(t, map[string]any{"session_id": sess.SessionID})); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result, _ := d.handleGoalGet(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
		goal := result.(map[string]any)["goal"].(sessionGoal)
		if goal.Status == "complete" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("auto goal did not complete")
}

func TestAutoGoalBlocksAfterThreeTerminalFailures(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	now := time.Now().UTC()
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "failed")
	d.sched.SetStatus(task.RunID, "failed")
	failed, _ := d.sched.Get(task.RunID)
	d.goals.mu.Lock()
	d.goals.goals[sess.SessionID] = &goalRecord{Goal: &sessionGoal{SessionID: sess.SessionID, Objective: "x", Status: "active", AutoContinue: true, ConsecutiveFailures: 2, LastTaskID: task.RunID, CreatedAt: now, UpdatedAt: now, MaxContinuations: 8, ActiveSince: now}}
	_ = d.goals.persistLocked()
	d.goals.mu.Unlock()
	d.reconcileGoalTask(failed)
	d.goals.mu.Lock()
	status := d.goals.goals[sess.SessionID].Goal.Status
	d.goals.mu.Unlock()
	if status != "blocked" {
		t.Fatalf("status=%s", status)
	}
}

func TestAutoGoalActivationDoesNotSurviveRestart(t *testing.T) {
	stateDir := t.TempDir()
	d := newDaemonAt(t, stateDir)
	sess, _ := d.store.CreateSession(t.TempDir(), "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, sess.WorkspaceRoot, "safe-edit", nil)
	now := time.Now().UTC()
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "done")
	d.sched.SetStatus(task.RunID, "completed")
	completed, _ := d.sched.Get(task.RunID)
	d.runs.save(completed)
	d.goals.mu.Lock()
	d.goals.goals[sess.SessionID] = &goalRecord{Goal: &sessionGoal{SessionID: sess.SessionID, Objective: "recover", Status: "active", AutoContinue: true, LastTaskID: task.RunID, CreatedAt: now, UpdatedAt: now, MaxContinuations: 8, ActiveSince: now}}
	_ = d.goals.persistLocked()
	d.goals.mu.Unlock()
	_ = d.Close()
	plantArmedGoalFile(t, stateDir, sess.SessionID, task.RunID, now)
	d = newDaemonAt(t, stateDir)
	defer d.Close()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		d.goals.mu.Lock()
		g := d.goals.goals[sess.SessionID].Goal
		status := g.Status
		armed := g.AutoContinue
		d.goals.mu.Unlock()
		if status != "active" {
			t.Fatalf("restart mutated goal status to %s", status)
		}
		if armed {
			t.Fatal("auto_continue stayed armed after restart")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(d.sched.List()); n != 1 {
		t.Fatalf("restart submitted extra tasks: %d", n)
	}
}

func TestAutoGoalDoesNotLaunchContinuationAfterRestart(t *testing.T) {
	stateDir := t.TempDir()
	d := newDaemonAt(t, stateDir)
	sess, _ := d.store.CreateSession(t.TempDir(), "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, sess.WorkspaceRoot, "safe-edit", nil)
	now := time.Now().UTC()
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "failed")
	d.sched.SetStatus(task.RunID, "failed")
	failed, _ := d.sched.Get(task.RunID)
	d.runs.save(failed)
	d.goals.mu.Lock()
	d.goals.goals[sess.SessionID] = &goalRecord{Goal: &sessionGoal{SessionID: sess.SessionID, Objective: "retry", Status: "active", AutoContinue: true, LastTaskID: task.RunID, CreatedAt: now, UpdatedAt: now, MaxContinuations: 8, ActiveSince: now}}
	_ = d.goals.persistLocked()
	d.goals.mu.Unlock()
	_ = d.Close()
	plantArmedGoalFile(t, stateDir, sess.SessionID, task.RunID, now)
	d = newDaemonAt(t, stateDir)
	defer d.Close()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := len(d.sched.List()); n != 1 {
			t.Fatalf("restart launched a continuation: %d tasks", n)
		}
		d.goals.mu.Lock()
		g := d.goals.goals[sess.SessionID].Goal
		status, armed, used := g.Status, g.AutoContinue, g.ContinuationsUsed
		d.goals.mu.Unlock()
		if status != "active" || armed || used != 0 {
			t.Fatalf("restart mutated goal: status=%s armed=%v continuations=%d", status, armed, used)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGoalContinuationFreezesBudgetBeforeDispatch(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	if _, err := d.handleGoalSet(mustJSON(t, map[string]any{"session_id": sess.SessionID, "objective": "ship", "token_budget": 17})); err != nil {
		t.Fatal(err)
	}
	result, err := d.handleGoalContinue(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	task := result.(*scheduler.ExecutionRun)
	if task.TokenBudget != 17 {
		t.Fatalf("returned task budget = %d, want 17", task.TokenBudget)
	}
	persisted, _ := d.sched.Get(task.RunID)
	if persisted.TokenBudget != 17 {
		t.Fatalf("published task budget = %d, want 17", persisted.TokenBudget)
	}
}

func TestGoalAuditFailureDoesNotChangeAuthority(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	_ = d.Kernel().Close()
	if _, err := d.handleGoalSet(mustJSON(t, map[string]any{"session_id": sess.SessionID, "objective": "must not persist"})); err == nil {
		t.Fatal("goal mutation succeeded without audit")
	}
	d.goals.mu.Lock()
	_, exists := d.goals.goals[sess.SessionID]
	d.goals.mu.Unlock()
	if exists {
		t.Fatal("goal authority changed after audit failure")
	}
}

func TestPlanModePersistsAcrossDaemonRestart(t *testing.T) {
	stateDir := t.TempDir()
	d := newDaemonAt(t, stateDir)
	sess, _ := d.store.CreateSession(t.TempDir(), "safe-edit")
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	d = newDaemonAt(t, stateDir)
	defer d.Close()
	if !d.isPlanMode(sess.SessionID) {
		t.Fatal("plan mode was not recovered from session state")
	}
}

func TestScheduleRPCsEnforceSessionOwnershipAndFreezeEnvelope(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	owner, _ := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	other, _ := d.store.CreateSession(t.TempDir(), "safe-edit")
	_, _ = d.store.SetNextModelPreference(owner.SessionID, "openai/gpt-5", "high")
	created, err := d.handleScheduleCreate(mustJSON(t, map[string]any{"session_id": owner.SessionID, "prompt": "run", "kind": "every", "expression": "1m"}))
	if err != nil {
		t.Fatal(err)
	}
	row := created.(*scheduler.Schedule)
	if row.Model != "openai/gpt-5" || row.ReasoningEffort != "high" || row.PermissionProfile != "safe-edit" || row.ApprovalMode != "on_request" || row.ConcurrencyPolicy != "forbid" {
		t.Fatalf("frozen envelope = %+v", row)
	}
	if _, err := d.handleSchedulePause(mustJSON(t, map[string]any{"session_id": other.SessionID, "schedule_id": row.ScheduleID})); err == nil {
		t.Fatal("cross-session schedule mutation succeeded")
	}
	listed, err := d.handleScheduleList(mustJSON(t, map[string]any{"session_id": other.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.(map[string]any)["schedules"].([]*scheduler.Schedule)) != 0 {
		t.Fatal("schedule list leaked another session")
	}
}

func TestScheduleOverlapQueuePersistsAndReplaceCancels(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	previous := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "slow")
	now := time.Now().UTC()
	queued, err := d.schedules.CreateWithEnvelope(scheduler.Schedule{SessionID: sess.SessionID, Prompt: "next", Kind: "every", Expression: "1m", ConcurrencyPolicy: "queue"}, now)
	if err != nil {
		t.Fatal(err)
	}
	d.schedules.MarkTask(queued.ScheduleID, previous.RunID)
	queued.LastTaskID = previous.RunID
	if !d.resolveScheduleOverlap(queued, now) {
		t.Fatal("queue did not defer overlap")
	}
	var persisted *scheduler.Schedule
	for _, row := range d.schedules.List() {
		if row.ScheduleID == queued.ScheduleID {
			persisted = row
		}
	}
	if persisted == nil || !persisted.Pending {
		t.Fatalf("queued state not persisted: %+v", persisted)
	}
	replace, err := d.schedules.CreateWithEnvelope(scheduler.Schedule{SessionID: sess.SessionID, Prompt: "replace", Kind: "every", Expression: "1m", ConcurrencyPolicy: "replace"}, now)
	if err != nil {
		t.Fatal(err)
	}
	d.schedules.MarkTask(replace.ScheduleID, previous.RunID)
	replace.LastTaskID = previous.RunID
	if d.resolveScheduleOverlap(replace, now) {
		t.Fatal("replace did not admit replacement")
	}
	cancelled, _ := d.sched.Get(previous.RunID)
	if cancelled.Status != "cancelled" {
		t.Fatalf("previous status=%s", cancelled.Status)
	}
}
