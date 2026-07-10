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
	if approved, ok := d.approveFromStoredGrant(sess, dec); ok {
		return approved, true
	}
	if d.interactiveApproval.Load() {
		resolved, granted, scope := d.awaitInteractiveApproval(sess, task, dec, label)
		if !granted {
			return dec, false
		}
		if resolved != nil {
			// The RPC handler that unblocked the wait (handleApprove /
			// handleDeny) already resolved this decision in the kernel —
			// re-approving it here would hit "no pending decision" (the
			// kernel's pending map is one-shot). Trust that resolution
			// instead of re-approving.
			return resolved, resolved.Decision == "allowed"
		}
		// Resolved via task.approval.resolve, which only signals the wait
		// and never touches the kernel: this call is the first and only
		// approval.
		approved, err := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, "operator", "")
		if err != nil || approved.Decision != "allowed" {
			return dec, false
		}
		if err := d.rememberApprovalGrant(sess, approved, scope, "operator", ""); err != nil {
			// The current one-time approval remains valid, but a failed durable
			// grant must never be treated as a broader scope.
			d.record(sess.SessionID, "ToolApproved", task.TaskID, "go", map[string]any{
				"status": "approval_grant_failed", "requested_scope": scope, "error": err.Error(),
			}, dec.DecisionID)
		}
		return approved, true
	}
	if !d.reviewAutonomousApproval(sess, task, dec, label) {
		return dec, false
	}
	approved, err := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, "agent", "")
	if err != nil || approved.Decision != "allowed" {
		return dec, false
	}
	return approved, true
}

// awaitInteractiveApproval pauses the task, emits a permission.request envelope,
// and blocks until an operator resolves it (task.approval.resolve or the
// task.action.approve / task.action.deny RPC surface), the timeout lapses
// (=> denied), or the daemon shuts down. Returns the already-kernel-resolved
// decision when the unblocking RPC call resolved one (nil if resolution was
// only signaled, not resolved), and whether the wait ended granted.
func (d *Daemon) awaitInteractiveApproval(sess *sessionstore.Session, task *scheduler.Task, dec *kernel.Decision, label string) (*kernel.Decision, bool, string) {
	ch := make(chan approvalSignal, 1)
	d.approvalMu.Lock()
	d.pendingApprovals[dec.DecisionID] = ch
	d.approvalMu.Unlock()
	defer func() {
		d.approvalMu.Lock()
		delete(d.pendingApprovals, dec.DecisionID)
		d.approvalMu.Unlock()
	}()

	d.sched.SetStatus(task.TaskID, "waiting_approval")
	ev := map[string]any{
		"type":        "permission.request",
		"session_id":  sess.SessionID,
		"task_id":     task.TaskID,
		"decision_id": dec.DecisionID,
		"capability":  dec.Capability,
		"resource":    dec.Resource,
		"reason":      dec.Reason,
		"label":       label,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	// A PatchApply decision's resource is the patch_id (registerPatchGate):
	// the operator's approval prompt must show the actual reviewable diff,
	// not just the capability name — otherwise the diff-rendering code in
	// go/tui (ColorDiff, openApproval reading ev["diff"]) never sees real
	// data and an operator approves content they cannot see.
	if dec.Capability == "PatchApply" {
		if patch, err := d.kern.PatchShow(sess.SessionID, dec.Resource); err == nil && patch != nil {
			ev["diff"] = patch.Diff
		}
	}
	// Persist the reviewable permission request before publishing it live. A
	// reconnect can reconcile this event with the later approval_resolved event
	// by decision_id without minting a second decision or approval prompt.
	if err := d.kern.RecordEvent(sess.SessionID, "ToolRequested", task.TaskID, "go", map[string]any{
		"status": "permission_requested", "decision_id": dec.DecisionID, "request": ev,
	}, dec.DecisionID); err != nil {
		d.sched.SetStatus(task.TaskID, "running")
		return nil, false, approvalScopeOnce
	}
	d.events.Publish(sess.SessionID, ev)

	timeout := d.approvalTimeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	var sig approvalSignal
	select {
	case sig = <-ch:
	case <-time.After(timeout):
		sig = approvalSignal{granted: false, scope: approvalScopeOnce}
	case <-d.stopCh:
		sig = approvalSignal{granted: false, scope: approvalScopeOnce}
	}
	d.sched.SetStatus(task.TaskID, "running")
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "operator",
		map[string]any{"status": "approval_resolved", "decision_id": dec.DecisionID, "granted": sig.granted, "scope": sig.scope}, dec.DecisionID)
	return sig.resolved, sig.granted, sig.scope
}

// approvalSignal carries an operator's verdict into a blocked
// awaitInteractiveApproval wait. resolved is set when the unblocking call
// already resolved the decision in the kernel (handleApprove / handleDeny),
// so the waiter must not re-approve; it is nil when the decision still needs
// resolving (task.approval.resolve, which only signals).
type approvalSignal struct {
	resolved *kernel.Decision
	granted  bool
	scope    string
}

// handleApprovalResolve records an operator's verdict for a pending interactive
// approval. Local-only: it is never on the remote allowlist.
func (d *Daemon) handleApprovalResolve(params json.RawMessage) (any, error) {
	var p struct {
		DecisionID string `json:"decision_id"`
		Approve    bool   `json:"approve"`
		Scope      string `json:"scope"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	scope, err := normalizeApprovalScope(p.Scope)
	if err != nil {
		return nil, err
	}
	if !p.Approve {
		scope = approvalScopeOnce
	}
	if p.Approve && scope != approvalScopeOnce {
		return nil, fmt.Errorf("scoped approval requires task.action.approve so grant persistence can be confirmed")
	}
	if !d.signalPendingApproval(p.DecisionID, nil, p.Approve, scope) {
		return nil, fmt.Errorf("no pending approval for decision %s", p.DecisionID)
	}
	return map[string]any{"decision_id": p.DecisionID, "resolved": p.Approve, "scope": scope}, nil
}

// signalPendingApproval unblocks an in-flight awaitInteractiveApproval wait
// for decisionID, if one is pending. It is the single choke point both
// task.approval.resolve (handleApprovalResolve) and the general-purpose
// task.action.approve / task.action.deny (handleApprove / handleDeny) funnel
// through, so however an operator's client resolves a requires_approval
// decision, a live interactive wait on that same decision actually unblocks
// with the matching outcome — audit and runtime can never disagree. resolved
// carries the already-kernel-resolved decision when the caller has one
// (handleApprove / handleDeny); pass nil when only signaling (unblocking a
// wait doesn't imply it was ever pending — most approvals resolve a
// synchronous RPC gate, e.g. a patch gate or a queued command, with nothing
// blocked in awaitInteractiveApproval, so a false return is not an error).
func (d *Daemon) signalPendingApproval(decisionID string, resolved *kernel.Decision, granted bool, scope string) bool {
	d.approvalMu.Lock()
	ch, ok := d.pendingApprovals[decisionID]
	d.approvalMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- approvalSignal{resolved: resolved, granted: granted, scope: scope}:
	default: // already resolved; ignore the duplicate
	}
	return true
}
