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
	d.sched.SetStatus(task.TaskID, "completed")
	d.runs.saveCheckpoint(task.TaskID, &runCheckpoint{Turn: 2, Transcript: &Transcript{Task: "parent task", Summary: "shared", Turns: []Turn{{Index: 1, ActionBrief: "read a.go", Obs: Observation{Content: "parent context"}}}, policy: defaultCompactionPolicy()}})

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
	if child.ForkedFromTaskID != task.TaskID || child.ForkedThroughTurn != 2 {
		t.Fatalf("fork lineage missing: %+v", child)
	}
}

func TestSessionForkRejectsBusyWithoutCreatingChild(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(parent.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "running")
	d.sched.SetStatus(task.TaskID, "running")
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
	d.sched.SetStatus(task.TaskID, "completed")
	cp1 := &runCheckpoint{Turn: 1, Transcript: &Transcript{Task: "parent", Turns: []Turn{{Index: 1, ActionBrief: "one", Obs: Observation{Content: "shared"}}}, policy: defaultCompactionPolicy()}}
	d.runs.saveCheckpoint(task.TaskID, cp1)
	cp2 := &runCheckpoint{Turn: 2, Transcript: &Transcript{Task: "parent", Turns: []Turn{{Index: 1, ActionBrief: "one", Obs: Observation{Content: "shared"}}, {Index: 2, ActionBrief: "two", Obs: Observation{Content: "parent-only"}}}, policy: defaultCompactionPolicy()}}
	d.runs.saveCheckpoint(task.TaskID, cp2)
	raw, _ := json.Marshal(map[string]any{"session_id": parent.SessionID, "last_task_id": task.TaskID, "through_turn": 1})
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
	parentAgain := d.runs.loadCheckpointTurn(task.TaskID, 1)
	if parentAgain.Transcript.Turns[0].Obs.Content != "shared" {
		t.Fatal("fork mutated parent checkpoint")
	}
}
