package daemon

import (
	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// maxEscalationsPerTask bounds how many times one subagent task may escalate a
// refused capability to its parent (an anti-abuse cap).
const maxEscalationsPerTask = 3

// escalatableCaps whitelists which capabilities a subagent may escalate. File
// reads are excluded (they're gated by the workspace boundary, not scarcity) and
// file writes are excluded (agentPatch has its own provenance guard).
var escalatableCaps = map[string]bool{"CommandExec": true, "PluginLoad": true}

func (d *Daemon) registerSubagentParent(childSessionID, parentTaskID string) {
	d.bridgeMu.Lock()
	d.subagentParentTask[childSessionID] = parentTaskID
	d.bridgeMu.Unlock()
}

func (d *Daemon) parentTaskFor(childSessionID string) string {
	d.bridgeMu.Lock()
	defer d.bridgeMu.Unlock()
	return d.subagentParentTask[childSessionID]
}

// incrEscalation returns true if the child task may escalate once more (under
// the per-task cap), consuming one from its budget.
func (d *Daemon) incrEscalation(childTaskID string) bool {
	d.bridgeMu.Lock()
	defer d.bridgeMu.Unlock()
	if d.escalationCounts[childTaskID] >= maxEscalationsPerTask {
		return false
	}
	d.escalationCounts[childTaskID]++
	return true
}

// escalateToParent re-evaluates a capability the child was refused under its
// DIRECT parent's live kernel session. The parent's real policy decides, so the
// child ⊆ parent invariant holds — a child can never gain more than the parent
// actually holds. Bounded: whitelisted capabilities only, one hop (the parent's
// requires_approval is resolved with plain resolveApproval, never re-entering the
// bridge, so no grandparent chaining), and a per-task cap.
func (d *Daemon) escalateToParent(child *sessionstore.Session, childTask *scheduler.Task, capability, resource, label string) (*kernel.Decision, bool) {
	if !escalatableCaps[capability] || child.ParentID == "" {
		return nil, false
	}
	parent, ok := d.store.Get(child.ParentID)
	if !ok || parent.Status != "active" {
		return nil, false
	}
	if !d.incrEscalation(childTask.TaskID) {
		d.record(child.SessionID, "TaskCreated", childTask.TaskID, "go",
			map[string]any{"status": "escalation_capped", "capability": capability}, "")
		return nil, false
	}

	parentTaskID := d.parentTaskFor(child.SessionID)
	parentTask, _ := d.sched.Get(parentTaskID)
	if parentTask == nil {
		parentTask = &scheduler.Task{TaskID: parentTaskID, SessionID: parent.SessionID}
	}

	parentDec, err := d.kern.Request(parent.SessionID, capability, resource, parentTaskID)
	if err != nil {
		return nil, false
	}
	d.record(child.SessionID, "TaskCreated", childTask.TaskID, "go", map[string]any{
		"status": "escalated_to_parent", "capability": capability,
		"parent_session": parent.SessionID, "parent_decision": parentDec.Decision,
	}, "")

	switch parentDec.Decision {
	case "allowed":
		return d.recordEscalationGrant(parent, parentTaskID, capability, child.SessionID, parentDec), true
	case "requires_approval":
		approved, ok := d.resolveApproval(parent, parentTask, parentDec,
			"escalated from subagent "+child.SessionID+": "+label)
		if !ok {
			return nil, false
		}
		return d.recordEscalationGrant(parent, parentTaskID, capability, child.SessionID, approved), true
	default: // denied — the parent doesn't hold it either
		return nil, false
	}
}

func (d *Daemon) recordEscalationGrant(parent *sessionstore.Session, parentTaskID, capability, childSessionID string, dec *kernel.Decision) *kernel.Decision {
	d.record(parent.SessionID, "ToolApproved", parentTaskID, "go", map[string]any{
		"status": "escalation_granted", "capability": capability, "from_child": childSessionID,
	}, dec.DecisionID)
	return dec
}

// resolveApprovalOrEscalate routes a requires_approval decision: a subagent
// escalates to its parent first (which may auto-approve or ask the operator); the
// main agent (no parent) uses the normal resolveApproval path. If escalation is
// refused or capped, the child falls back to resolving under its own session.
func (d *Daemon) resolveApprovalOrEscalate(sess *sessionstore.Session, task *scheduler.Task, dec *kernel.Decision, capability, resource, label string) (*kernel.Decision, bool) {
	if sess.ParentID != "" {
		if esc, ok := d.escalateToParent(sess, task, capability, resource, label); ok {
			return esc, true
		}
	}
	return d.resolveApproval(sess, task, dec, label)
}
