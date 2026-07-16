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
//
// Product naming: interactive_approval=true is "ask"; false is "always-approve"
// (Grok-style auto-approve of requires_approval). Deny rules and plan mode still apply.
func (d *Daemon) SetInteractiveApproval(on bool) { d.interactiveApproval.Store(on) }

// handleSetInteractiveApproval is the governed RPC for operator toggles.
// Params: { "on": bool } — true = ask (pause), false = always-approve (auto).
func (d *Daemon) handleSetInteractiveApproval(params json.RawMessage) (any, error) {
	var p struct {
		On        *bool  `json:"on"`
		SessionID string `json:"session_id"`
	}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.On == nil {
		return nil, fmt.Errorf("on boolean is required")
	}
	prev := d.interactiveApproval.Load()
	d.interactiveApproval.Store(*p.On)
	mode := "ask"
	if !*p.On {
		mode = "always-approve"
	}
	prevMode := "ask"
	if !prev {
		prevMode = "always-approve"
	}
	// Audit even without session — attach session_id when the TUI provides one.
	payload := map[string]any{
		"interactive_approval": *p.On,
		"approval_mode":        mode,
		"previous_mode":        prevMode,
		"warning":              "always-approve auto-allows requires_approval tool calls; deny rules, plan mode, and sandbox still apply",
	}
	sid := p.SessionID
	d.record(sid, "InteractiveApprovalChanged", "", "operator", payload, "")
	return map[string]any{
		"interactive_approval": *p.On,
		"approval_mode":        mode,
		"previous_mode":        prevMode,
		"warning":              payload["warning"],
	}, nil
}

// resolveApproval turns a requires_approval decision into a final one. In
// autonomous mode (default) it auto-approves as the agent. In interactive mode
// it asks the operator and only approves on an explicit allow. Returns the
// (possibly upgraded) decision and whether it is now allowed.
func (d *Daemon) resolveApproval(sess *sessionstore.Session, task *scheduler.Task, dec *kernel.Decision, label string) (*kernel.Decision, bool) {
	if err := d.markActiveToolApprovalRequired(task.TaskID, dec.DecisionID); err != nil {
		d.closePendingApproval(sess, task, dec, "denied", "approval lifecycle could not be persisted")
		return dec, false
	}
	if approved, ok := d.approveFromStoredGrant(sess, dec); ok {
		if err := d.ensureActiveToolStarted(task.TaskID); err != nil {
			return dec, false
		}
		return approved, true
	}
	if d.interactiveApproval.Load() {
		resolved, granted, scope, terminal := d.awaitInteractiveApproval(sess, task, dec, label)
		if !granted {
			if resolved == nil {
				status := "denied"
				reason := "operator denied approval"
				switch terminal {
				case "timed_out":
					status, reason = "expired", "approval request timed out"
				case "cancelled":
					reason = "task was cancelled while awaiting approval"
				}
				d.closePendingApproval(sess, task, dec, status, reason)
			}
			return dec, false
		}
		if resolved != nil {
			// The RPC handler that unblocked the wait (handleApprove /
			// handleDeny) already resolved this decision in the kernel —
			// re-approving it here would hit "no pending decision" (the
			// kernel's pending map is one-shot). Trust that resolution
			// instead of re-approving.
			if resolved.Decision == "allowed" {
				if err := d.ensureActiveToolStarted(task.TaskID); err != nil {
					return dec, false
				}
			}
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
		if err := d.ensureActiveToolStarted(task.TaskID); err != nil {
			return dec, false
		}
		return approved, true
	}
	if !d.reviewAutonomousApproval(sess, task, dec, label) {
		d.closePendingApproval(sess, task, dec, "denied", "autonomous risk review denied approval")
		return dec, false
	}
	approved, err := d.kern.ApproveWithRole(sess.SessionID, dec.DecisionID, "agent", "")
	if err != nil || approved.Decision != "allowed" {
		return dec, false
	}
	if err := d.ensureActiveToolStarted(task.TaskID); err != nil {
		return dec, false
	}
	return approved, true
}

// closePendingApproval fail-closes an unresolved kernel decision and mirrors
// that terminal state into daemon-side queues. In particular, a timed-out
// agent patch must not leave an approvable patch gate behind.
func (d *Daemon) closePendingApproval(sess *sessionstore.Session, task *scheduler.Task, dec *kernel.Decision, gateStatus, reason string) {
	if dec == nil || dec.DecisionID == "" {
		return
	}
	d.mu.Lock()
	delete(d.pendingCmds, dec.DecisionID)
	delete(d.pendingMemWrites, dec.DecisionID)
	delete(d.pendingMemControls, dec.DecisionID)
	if patchID, ok := d.patchGateByDecision[dec.DecisionID]; ok {
		if gate := d.patchGates[patchID]; gate != nil && gate.sessionID == sess.SessionID && gate.status == "requires_approval" {
			gate.status = gateStatus
		}
	}
	d.mu.Unlock()
	if _, err := d.kern.Deny(sess.SessionID, dec.DecisionID, "system", reason); err != nil {
		d.record(sess.SessionID, "PolicyViolation", task.TaskID, "go", map[string]any{
			"capability": dec.Capability, "decision_id": dec.DecisionID,
			"refusal": "approval_close_failed", "error": err.Error(),
		}, dec.DecisionID)
	}
}

// awaitInteractiveApproval pauses the task, emits a permission.request envelope,
// and blocks until an operator resolves it (task.approval.resolve or the
// task.action.approve / task.action.deny RPC surface), the timeout lapses
// (=> denied), or the daemon shuts down. Returns the already-kernel-resolved
// decision when the unblocking RPC call resolved one (nil if resolution was
// only signaled, not resolved), and whether the wait ended granted.
func (d *Daemon) awaitInteractiveApproval(sess *sessionstore.Session, task *scheduler.Task, dec *kernel.Decision, label string) (*kernel.Decision, bool, string, string) {
	ch := make(chan approvalSignal, 1)
	d.approvalMu.Lock()
	d.pendingApprovals[dec.DecisionID] = ch
	d.approvalMu.Unlock()
	removePending := func() {
		d.approvalMu.Lock()
		delete(d.pendingApprovals, dec.DecisionID)
		d.approvalMu.Unlock()
	}
	defer removePending()

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
	cursor, err := d.kern.RecordEventWithCursor(sess.SessionID, "ToolRequested", task.TaskID, "go", map[string]any{
		"status": "permission_requested", "decision_id": dec.DecisionID, "request": ev,
	}, dec.DecisionID)
	if err != nil {
		d.sched.SetStatus(task.TaskID, "running")
		return nil, false, approvalScopeOnce, ""
	}
	ev[internalRawAuditCursor] = cursor
	d.events.Publish(sess.SessionID, ev)

	timeout := d.approvalTimeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	var sig approvalSignal
	select {
	case sig = <-ch:
	case <-d.contextForTask(task.TaskID).Done():
		sig = approvalSignal{granted: false, scope: approvalScopeOnce, terminal: "cancelled"}
		d.markActiveToolTerminal(task.TaskID, "cancelled")
	case <-time.After(timeout):
		sig = approvalSignal{granted: false, scope: approvalScopeOnce, terminal: "timed_out"}
		d.markActiveToolTerminal(task.TaskID, "timed_out")
	case <-d.stopCh:
		sig = approvalSignal{granted: false, scope: approvalScopeOnce, terminal: "cancelled"}
		d.markActiveToolTerminal(task.TaskID, "cancelled")
	}
	// Make timeout/cancellation final for task.approval.resolve before the
	// kernel decision is closed by resolveApproval.
	if !sig.granted {
		removePending()
	}
	if d.activeToolTerminal(task.TaskID) == "" {
		d.sched.SetStatus(task.TaskID, "running")
	}
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "operator",
		map[string]any{"status": "approval_resolved", "decision_id": dec.DecisionID, "granted": sig.granted, "scope": sig.scope}, dec.DecisionID)
	return sig.resolved, sig.granted, sig.scope, sig.terminal
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
	terminal string
}

// handleApprovalResolve records an operator's verdict for a pending interactive
// approval. Local-only: it is never on the remote allowlist.
func (d *Daemon) handleApprovalResolve(params json.RawMessage) (any, error) {
	var p struct {
		DecisionID string `json:"decision_id"`
		Approve    *bool  `json:"approve"`
		Allow      *bool  `json:"allow"`
		Scope      string `json:"scope"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	scope, err := normalizeApprovalScope(p.Scope)
	if err != nil {
		return nil, err
	}
	if p.Approve == nil && p.Allow == nil {
		return nil, fmt.Errorf("approve is required")
	}
	if p.Approve != nil && p.Allow != nil && *p.Approve != *p.Allow {
		return nil, fmt.Errorf("approve and legacy allow conflict")
	}
	approve := p.Approve
	if approve == nil {
		approve = p.Allow
	}
	if !*approve {
		scope = approvalScopeOnce
	}
	if *approve && scope != approvalScopeOnce {
		return nil, fmt.Errorf("scoped approval requires task.action.approve so grant persistence can be confirmed")
	}
	if !d.signalPendingApproval(p.DecisionID, nil, *approve, scope) {
		return nil, fmt.Errorf("no pending approval for decision %s", p.DecisionID)
	}
	return map[string]any{"decision_id": p.DecisionID, "resolved": *approve, "scope": scope}, nil
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
