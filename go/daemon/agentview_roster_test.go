package daemon

import (
	"testing"

	"github.com/Nebutra/carina/go/agentview"
)

func TestAgentRecapAcceptsTaskIDAlias(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "inspect", "", "explore", nil)
	if err := d.agentView.Set(sess.SessionID, agentview.Metadata{WorktreeID: "wt-test"}); err != nil {
		t.Fatal(err)
	}

	peek, err := d.handleAgentPeek(mustJSON(t, map[string]any{"task_id": task.RunID}))
	if err != nil {
		t.Fatal(err)
	}
	entry := peek.(agentview.Entry)
	if entry.TaskID != task.RunID || entry.Agent != "explore" || entry.WorktreeID != "wt-test" {
		t.Fatalf("peek = %+v", entry)
	}

	recap, err := d.handleAgentRecap(mustJSON(t, map[string]any{"task_id": task.RunID}))
	if err != nil {
		t.Fatal(err)
	}
	payload := recap.(map[string]any)
	if _, ok := payload["recent_events"]; !ok {
		t.Fatalf("recap missing events: %#v", recap)
	}
	got := payload["agent"].(agentview.Entry)
	if got.RunID != task.RunID || got.Profile != "safe-edit" {
		t.Fatalf("recap agent = %+v", got)
	}
}

func TestAgentRosterListsChildWithAttenuation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	child, err := d.createSubSession(ws, "read-only", parent.ApprovalMode, parent.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalModelAgent(child.SessionID, child.WorkspaceID, "look around", "", "explore", nil)
	roster := d.agentRoster()
	found := false
	for _, entry := range append(append(roster.NeedsInput, roster.Working...), roster.Completed...) {
		if entry.RunID != task.RunID {
			continue
		}
		found = true
		if entry.ParentID != parent.SessionID || entry.Profile != "read-only" || entry.Agent != "explore" {
			t.Fatalf("child roster = %+v", entry)
		}
	}
	if !found {
		t.Fatalf("child run missing from roster: %+v", roster)
	}
}
