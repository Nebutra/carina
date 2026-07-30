package daemon

import (
	"encoding/json"
	"testing"

	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// TestSessionForkLineage: forking a session yields a new session that shares the
// workspace and is linked to the source as its parent.
func TestSessionForkLineage(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(parent.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "parent task")
	d.sched.SetStatus(task.RunID, "completed")
	d.runs.saveCheckpoint(task.RunID, &runCheckpoint{Turn: 2, Transcript: &Transcript{Task: "parent task", Summary: "shared", Turns: []Turn{{Index: 1, ActionBrief: "read a.go", Obs: Observation{Content: "parent context"}}}, policy: defaultCompactionPolicy()}})

	raw, _ := json.Marshal(map[string]any{"session_id": parent.SessionID})
	res, err := d.handleSessionFork(raw)
	if err != nil {
		t.Fatal(err)
	}
	child := res.(*sessionstore.Session)
	if child.ParentID != parent.SessionID {
		t.Fatalf("fork should link to parent, got parent_id=%q", child.ParentID)
	}
	if child.WorkspaceRoot != ws {
		t.Fatalf("fork should share the workspace, got %q", child.WorkspaceRoot)
	}
	if child.SessionID == parent.SessionID {
		t.Fatal("fork must be a new session")
	}
	if child.ForkedFromTaskID != task.RunID || child.ForkedThroughTurn != 2 {
		t.Fatalf("fork lineage missing: %+v", child)
	}
}

func TestSessionForkPreservesSourceSettingsAndIsIdempotent(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "never")
	parent, _ = d.store.SetNextModelPreference(parent.SessionID, "openai/gpt-5", "high")
	parent, _ = d.store.SetPlanMode(parent.SessionID, true)
	d.setPlanMode(parent.SessionID, true)
	d.kern.InitSessionFull(parent.SessionID, ws, parent.PermissionProfile, parent.ApprovalMode, d.org)
	task := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "parent task")
	d.sched.SetStatus(task.RunID, "completed")
	d.runs.saveCheckpoint(task.RunID, &runCheckpoint{Turn: 1, Transcript: &Transcript{Task: "parent task", policy: defaultCompactionPolicy()}})

	params := mustJSON(t, map[string]any{
		"session_id": parent.SessionID, "last_task_id": task.RunID, "client_fork_id": "history-edit-1",
	})
	firstAny, err := d.handleSessionFork(params)
	if err != nil {
		t.Fatal(err)
	}
	secondAny, err := d.handleSessionFork(params)
	if err != nil {
		t.Fatal(err)
	}
	first, second := firstAny.(*sessionstore.Session), secondAny.(*sessionstore.Session)
	if first.SessionID != second.SessionID || len(d.store.List()) != 2 {
		t.Fatalf("idempotent fork created duplicates: first=%+v second=%+v", first, second)
	}
	if first.PermissionProfile != parent.PermissionProfile || first.ApprovalMode != parent.ApprovalMode ||
		first.NextModel != parent.NextModel || first.NextReasoningEffort != parent.NextReasoningEffort || !first.PlanMode {
		t.Fatalf("fork lost source settings: parent=%+v child=%+v", parent, first)
	}
	if !d.planModeEnabled(first.SessionID) {
		t.Fatal("forked plan mode was not activated in daemon state")
	}
	if first.ForkRequestID != "history-edit-1" || first.ForkFingerprint == "" {
		t.Fatalf("fork identity was not persisted: %+v", first)
	}
}

func TestSessionForkBeforeFirstPromptPreservesSourceAndRejectsIdentityReuse(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	parent, _ = d.store.SetNextModelPreference(parent.SessionID, "openai/gpt-5", "medium")
	d.kern.InitSessionFull(parent.SessionID, ws, parent.PermissionProfile, parent.ApprovalMode, d.org)

	firstAny, err := d.handleSessionFork(mustJSON(t, map[string]any{
		"session_id": parent.SessionID, "before_first": true, "client_fork_id": "history-edit-first",
	}))
	if err != nil {
		t.Fatal(err)
	}
	child := firstAny.(*sessionstore.Session)
	if child.ParentID != parent.SessionID || child.ForkedFromTaskID != "" || child.ForkedThroughTurn != 0 {
		t.Fatalf("before-first lineage is wrong: %+v", child)
	}
	if child.NextModel != parent.NextModel || child.NextReasoningEffort != parent.NextReasoningEffort {
		t.Fatalf("before-first fork lost model settings: %+v", child)
	}

	if _, err := d.handleSessionFork(mustJSON(t, map[string]any{
		"session_id": parent.SessionID, "before_first": true, "last_task_id": "task_1",
	})); err == nil {
		t.Fatal("before_first accepted a conflicting task boundary")
	}
	if _, err := d.handleSessionFork(mustJSON(t, map[string]any{
		"session_id": parent.SessionID, "last_task_id": "task_other", "client_fork_id": "history-edit-first",
	})); err == nil {
		t.Fatal("client_fork_id reuse for a different boundary was accepted")
	}
}

func TestSessionForkIdempotencySurvivesDaemonRestart(t *testing.T) {
	stateDir := t.TempDir()
	ws := t.TempDir()
	d := newDaemonAt(t, stateDir)
	parent, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, parent.PermissionProfile, parent.ApprovalMode, d.org)
	params := mustJSON(t, map[string]any{
		"session_id": parent.SessionID, "before_first": true, "client_fork_id": "restart-stable-fork",
	})
	firstAny, err := d.handleSessionFork(params)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(*sessionstore.Session)
	// This boundary simulates process replacement, not a graceful-shutdown
	// assertion. Close may report the intentionally killed kernel helper.
	_ = d.Close()

	d = newDaemonAt(t, stateDir)
	defer d.Close()
	retryAny, err := d.handleSessionFork(params)
	if err != nil {
		t.Fatal(err)
	}
	retry := retryAny.(*sessionstore.Session)
	if retry.SessionID != first.SessionID || len(d.store.List()) != 2 {
		t.Fatalf("restart retry created a duplicate: first=%+v retry=%+v", first, retry)
	}
}

func TestSessionForkRejectsBusyWithoutCreatingChild(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(parent.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "running")
	d.sched.SetStatus(task.RunID, "running")
	before := len(d.store.List())
	raw, _ := json.Marshal(map[string]any{"session_id": parent.SessionID})
	if _, err := d.handleSessionFork(raw); err == nil {
		t.Fatal("busy fork accepted")
	}
	if len(d.store.List()) != before {
		t.Fatal("busy fork created a child session")
	}
}

func TestSessionForkThroughTurnCopiesStableContextAndIsolatesWrites(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(parent.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "parent")
	d.sched.SetStatus(task.RunID, "completed")
	cp1 := &runCheckpoint{Turn: 1, Transcript: &Transcript{Task: "parent", Turns: []Turn{{Index: 1, ActionBrief: "one", Obs: Observation{Content: "shared"}}}, policy: defaultCompactionPolicy()}}
	d.runs.saveCheckpoint(task.RunID, cp1)
	cp2 := &runCheckpoint{Turn: 2, Transcript: &Transcript{Task: "parent", Turns: []Turn{{Index: 1, ActionBrief: "one", Obs: Observation{Content: "shared"}}, {Index: 2, ActionBrief: "two", Obs: Observation{Content: "parent-only"}}}, policy: defaultCompactionPolicy()}}
	d.runs.saveCheckpoint(task.RunID, cp2)
	raw, _ := json.Marshal(map[string]any{"session_id": parent.SessionID, "last_task_id": task.RunID, "through_turn": 1})
	res, err := d.handleSessionFork(raw)
	if err != nil {
		t.Fatal(err)
	}
	child := res.(*sessionstore.Session)
	inherited := d.runs.loadCheckpointTurn(child.ForkedFromTaskID, child.ForkedThroughTurn)
	if inherited == nil || len(inherited.Transcript.Turns) != 1 || inherited.Transcript.Turns[0].Obs.Content != "shared" {
		t.Fatalf("wrong inherited context: %+v", inherited)
	}
	inherited.Transcript.Turns[0].Obs.Content = "child mutation"
	parentAgain := d.runs.loadCheckpointTurn(task.RunID, 1)
	if parentAgain.Transcript.Turns[0].Obs.Content != "shared" {
		t.Fatal("fork mutated parent checkpoint")
	}
}
