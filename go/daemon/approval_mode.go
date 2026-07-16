package daemon

import (
	"fmt"
	"strings"
)

// Product approval modes control what happens when the kernel returns
// requires_approval (after exact stored grants). Orthogonal to session
// ApprovalMode untrusted|on_request|never, which is a kernel policy axis.
const (
	approvalModeAsk           = "ask"
	approvalModeAlwaysApprove = "always-approve"
	approvalModeDontAsk       = "dont-ask"
)

// normalizeApprovalMode accepts product names and common aliases (Grok/CC
// dontAsk, yolo). Empty becomes ask so interactive surfaces default to
// pausing for an operator rather than silent auto-approve.
func normalizeApprovalMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", approvalModeAsk, "interactive", "on_request", "on-request":
		return approvalModeAsk, nil
	case approvalModeAlwaysApprove, "always_approve", "alwaysapprove", "yolo", "bypass", "bypasspermissions", "never":
		// "never" here is product always-approve (auto-run requires_approval),
		// not the session kernel axis name alone — callers that mean kernel
		// never must not route through this helper.
		return approvalModeAlwaysApprove, nil
	case approvalModeDontAsk, "dont_ask", "dontask", "deny-by-default", "deny_by_default":
		return approvalModeDontAsk, nil
	default:
		return "", fmt.Errorf("approval_mode must be one of ask, always-approve, dont-ask")
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

// SetApprovalMode is the test/entrypoint surface for the three-way product mode.
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
