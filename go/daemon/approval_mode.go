package daemon

import (
	"fmt"
	"strings"
)

// Product HITL modes control what happens when the kernel returns
// requires_approval (after exact stored grants).
//
// Orthogonal axes — do not conflate:
//
//	Product (this file / daemon approval_mode / /approval-mode):
//	  ask | always-approve | dont-ask | accept-edits
//	Session/kernel (session.approval_mode / InitSessionFull):
//	  untrusted | on_request | never
//
// Session "never" means the kernel auto-allows requires_approval before the
// daemon HITL path. Product "always-approve" means the daemon auto-allows after
// the kernel still returned requires_approval (plus risk_review). They are not
// interchangeable names.
const (
	approvalModeAsk           = "ask"
	approvalModeAlwaysApprove = "always-approve"
	approvalModeDontAsk       = "dont-ask"
	approvalModeAcceptEdits   = "accept-edits"
)

// normalizeApprovalMode accepts product names and a small set of product
// aliases (yolo, bypass, dontAsk, acceptEdits). Empty becomes ask so interactive
// surfaces default to pausing for an operator rather than silent auto-approve.
//
// Session-axis tokens (untrusted|on_request|never) are rejected with an
// explicit error so they cannot silently map to a different product mode.
func normalizeApprovalMode(mode string) (string, error) {
	raw := strings.TrimSpace(mode)
	switch strings.ToLower(raw) {
	case "", approvalModeAsk, "interactive":
		return approvalModeAsk, nil
	case approvalModeAlwaysApprove, "always_approve", "alwaysapprove", "yolo", "bypass", "bypasspermissions":
		return approvalModeAlwaysApprove, nil
	case approvalModeDontAsk, "dont_ask", "dontask", "deny-by-default", "deny_by_default":
		return approvalModeDontAsk, nil
	case approvalModeAcceptEdits, "accept_edits", "acceptedits", "acceptEdits":
		// acceptEdits is case-normalized by ToLower; keep explicit common spellings.
		return approvalModeAcceptEdits, nil
	case "never", "untrusted", "on_request", "on-request":
		return "", fmt.Errorf("%q is a session/kernel approval axis (untrusted|on_request|never), not product HITL mode; use ask|always-approve|dont-ask|accept-edits", raw)
	default:
		return "", fmt.Errorf("product approval_mode must be one of ask, always-approve, dont-ask, accept-edits")
	}
}

// isEditCapability reports whether a requires_approval decision is a workspace
// file edit (Grok/CC acceptEdits scope). Shell/network/secrets still prompt.
func isEditCapability(capability string) bool {
	switch strings.ToLower(strings.TrimSpace(capability)) {
	case "filewrite", "patchapply":
		return true
	default:
		return false
	}
}

// approvalModeFromInteractive maps the legacy boolean: true=ask, false=always-approve.
func approvalModeFromInteractive(interactive bool) string {
	if interactive {
		return approvalModeAsk
	}
	return approvalModeAlwaysApprove
}

func interactiveFromApprovalMode(mode string) bool {
	// Only pure "ask" mirrors the legacy interactive_approval=true flag.
	// accept-edits still prompts for non-edit tools but is not fully interactive.
	return mode == approvalModeAsk
}

func (d *Daemon) approvalModeString() string {
	if v := d.approvalMode.Load(); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	// Fallback for partially-initialized tests.
	return approvalModeFromInteractive(d.interactiveApproval.Load())
}

// setApprovalMode stores mode and keeps interactiveApproval in sync for
// legacy readers (best_of_n cost gate, inventory bool, etc.).
func (d *Daemon) setApprovalMode(mode string) error {
	normalized, err := normalizeApprovalMode(mode)
	if err != nil {
		return err
	}
	if normalized == approvalModeAlwaysApprove && d.disableAlwaysApprove.Load() {
		return fmt.Errorf("always-approve is disabled by organization policy (disable_always_approve)")
	}
	d.approvalMode.Store(normalized)
	d.interactiveApproval.Store(interactiveFromApprovalMode(normalized))
	return nil
}

// SetApprovalMode is the test/entrypoint surface for the product HITL mode.
func (d *Daemon) SetApprovalMode(mode string) error {
	return d.setApprovalMode(mode)
}

// SetDisableAlwaysApprove locks or unlocks always-approve at runtime (managed
// config / org policy). When locking while currently always-approve, falls
// back to ask so the daemon cannot remain in a forbidden mode.
func (d *Daemon) SetDisableAlwaysApprove(disabled bool) {
	d.disableAlwaysApprove.Store(disabled)
	if disabled && d.approvalModeString() == approvalModeAlwaysApprove {
		_ = d.setApprovalMode(approvalModeAsk)
	}
}

// SetInteractiveApproval toggles human-in-the-loop approval (legacy boolean API).
// When on, mode is ask; when off, mode is always-approve (subject to org lock).
func (d *Daemon) SetInteractiveApproval(on bool) {
	mode := approvalModeAlwaysApprove
	if on {
		mode = approvalModeAsk
	}
	if err := d.setApprovalMode(mode); err != nil && on {
		// Enabling ask never fails the org lock.
		_ = d.setApprovalMode(approvalModeAsk)
	} else if err != nil {
		// Disabling interactive (always-approve) blocked — stay put.
		return
	}
}
