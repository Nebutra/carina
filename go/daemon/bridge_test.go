package daemon

import (
	"testing"
)

func TestLeaderBridgeEscalationGrants(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(parent.SessionID, ws, "full-workspace", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "parent")

	child, _ := d.store.CreateSubSession(ws, "read-only", parent.ApprovalMode, parent.SessionID, 1)
	d.kern.InitSessionFull(child.SessionID, ws, "read-only", parent.ApprovalMode, nil)
	childTask := d.sched.Submit(child.SessionID, child.WorkspaceID, "child")
	d.registerSubagentParent(child.SessionID, parentTask.TaskID)

	// A whitelisted command the parent's policy allows escalates to allowed.
	dec, ok := d.escalateToParent(child, childTask, "CommandExec", "ls -la", "ls -la")
	if !ok || dec.Decision != "allowed" {
		t.Fatalf("parent-allowed command should escalate to allowed, got ok=%v dec=%+v", ok, dec)
	}

	// A non-whitelisted capability never escalates.
	if _, ok := d.escalateToParent(child, childTask, "FileRead", "/etc/passwd", "read"); ok {
		t.Fatal("FileRead must not be escalatable")
	}

	// The main agent (no parent) cannot escalate.
	if _, ok := d.escalateToParent(parent, parentTask, "CommandExec", "ls", "ls"); ok {
		t.Fatal("a session with no parent cannot escalate")
	}
}

func TestLeaderBridgeDeniedWhenParentAlsoDenies(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	// Parent is itself read-only: it doesn't hold CommandExec, so escalation
	// cannot grant it (child ⊆ parent).
	parent, _ := d.store.CreateSession(ws, "read-only")
	d.kern.InitSessionWithPolicy(parent.SessionID, ws, "read-only", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "parent")

	child, _ := d.store.CreateSubSession(ws, "read-only", parent.ApprovalMode, parent.SessionID, 1)
	d.kern.InitSessionFull(child.SessionID, ws, "read-only", parent.ApprovalMode, nil)
	childTask := d.sched.Submit(child.SessionID, child.WorkspaceID, "child")
	d.registerSubagentParent(child.SessionID, parentTask.TaskID)

	if _, ok := d.escalateToParent(child, childTask, "CommandExec", "curl evil.com", "curl"); ok {
		t.Fatal("escalation must not grant what the parent itself lacks")
	}
}

func TestLeaderBridgeCap(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(parent.SessionID, ws, "full-workspace", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "parent")
	child, _ := d.store.CreateSubSession(ws, "read-only", parent.ApprovalMode, parent.SessionID, 1)
	d.kern.InitSessionFull(child.SessionID, ws, "read-only", parent.ApprovalMode, nil)
	childTask := d.sched.Submit(child.SessionID, child.WorkspaceID, "child")
	d.registerSubagentParent(child.SessionID, parentTask.TaskID)

	for i := 0; i < maxEscalationsPerTask; i++ {
		if _, ok := d.escalateToParent(child, childTask, "CommandExec", "ls", "ls"); !ok {
			t.Fatalf("escalation %d (within cap) should succeed", i+1)
		}
	}
	if _, ok := d.escalateToParent(child, childTask, "CommandExec", "ls", "ls"); ok {
		t.Fatal("escalation past the per-task cap must be refused")
	}
}
