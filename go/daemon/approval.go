package daemon

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// defaultApprovalTimeout bounds how long an interactive approval blocks before
// defaulting to denied (so a run can't hang forever awaiting an absent operator).
const defaultApprovalTimeout = 5 * time.Minute

// SetInteractiveApproval toggles human-in-the-loop approval (used by tests and
// the entrypoint). When on, a requires_approval decision pauses for an operator
// verdict instead of being auto-approved.
func (d *Daemon) SetInteractiveApproval(on bool) { d.interactiveApproval.Store(on) }

// resolveApproval turns a requires_approval decision into a final one. In
// autonomous mode (default) it auto-approves as the agent. In interactive mode
// it asks the operator and only approves on an explicit allow. Returns the
// (possibly upgraded) decision and whether it is now allowed.
func (d *Daemon) resolveApproval(sess *sessionstore.Session, task *scheduler.Task, dec *kernel.Decision, label string) (*kernel.Decision, bool) {
	approver := "agent"
	if d.interactiveApproval.Load() {
		if !d.awaitInteractiveApproval(sess, task, dec, label) {
			return dec, false
		}
		approver = "operator"
	}
	approved, err := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, approver, "")
	if err != nil || approved.Decision != "allowed" {
		return dec, false
	}
	return approved, true
}

// awaitInteractiveApproval pauses the task, emits a permission.request envelope,
// and blocks until an operator resolves it (task.approval.resolve), the timeout
// lapses (=> denied), or the daemon shuts down.
func (d *Daemon) awaitInteractiveApproval(sess *sessionstore.Session, task *scheduler.Task, dec *kernel.Decision, label string) bool {
	ch := make(chan bool, 1)
	d.approvalMu.Lock()
	d.pendingApprovals[dec.DecisionID] = ch
	d.approvalMu.Unlock()
	defer func() {
		d.approvalMu.Lock()
		delete(d.pendingApprovals, dec.DecisionID)
		d.approvalMu.Unlock()
	}()

	d.sched.SetStatus(task.TaskID, "waiting_approval")
	d.events.Publish(sess.SessionID, map[string]any{
		"type":        "permission.request",
		"session_id":  sess.SessionID,
		"task_id":     task.TaskID,
		"decision_id": dec.DecisionID,
		"capability":  dec.Capability,
		"resource":    dec.Resource,
		"reason":      dec.Reason,
		"label":       label,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	timeout := d.approvalTimeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	var granted bool
	select {
	case granted = <-ch:
	case <-time.After(timeout):
		granted = false
	case <-d.stopCh:
		granted = false
	}
	d.sched.SetStatus(task.TaskID, "running")
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "operator",
		map[string]any{"status": "approval_resolved", "decision_id": dec.DecisionID, "granted": granted}, dec.DecisionID)
	return granted
}

// handleApprovalResolve records an operator's verdict for a pending interactive
// approval. Local-only: it is never on the remote allowlist.
func (d *Daemon) handleApprovalResolve(params json.RawMessage) (any, error) {
	var p struct {
		DecisionID string `json:"decision_id"`
		Approve    bool   `json:"approve"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	d.approvalMu.Lock()
	ch, ok := d.pendingApprovals[p.DecisionID]
	d.approvalMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no pending approval for decision %s", p.DecisionID)
	}
	select {
	case ch <- p.Approve:
	default: // already resolved; ignore the duplicate
	}
	return map[string]any{"decision_id": p.DecisionID, "resolved": p.Approve}, nil
}
