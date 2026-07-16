package daemon

import (
	"context"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// buildTaskMemoryEvidence is the only fresh-task HMS recall boundary. Its
// result becomes a pinned, low-trust tool observation in the checkpointed
// transcript, so resume, fork, and restore never re-query mutable remote state.
func (d *Daemon) buildTaskMemoryEvidence(ctx context.Context, sess *sessionstore.Session, task *scheduler.Task) string {
	provider := d.memoryHMS
	if provider == nil {
		return ""
	}
	resource := strings.ToLower(provider.endpoint.Hostname())
	decision, err := d.kern.Request(sess.SessionID, "NetworkAccess", resource, task.TaskID)
	if err == nil && decision.Decision == "requires_approval" {
		if approved, ok := d.approveFromStoredGrant(sess, decision); ok {
			decision = approved
		}
	}
	if err != nil || decision.Decision != "allowed" {
		reason := "network_policy_denied"
		if err != nil {
			reason = "network_policy_error"
		} else if decision.Decision == "requires_approval" {
			_, _ = d.kern.Deny(sess.SessionID, decision.DecisionID, "system", "HMS recall has no stored NetworkAccess grant")
		}
		provider.markPolicyDenied(reason)
		d.recordMemoryRecall(sess, task, "degraded", reason, 0)
		return ""
	}
	provider.markAuthorized()
	externalizeResource := "provider=hms host=" + resource + " query_sha256=" + hashMemoryQuery(task.UserPrompt) + " targets=user,memory"
	externalize, err := d.kern.Request(sess.SessionID, "MemoryExternalize", externalizeResource, task.TaskID)
	if err == nil && externalize.Decision == "requires_approval" {
		if approved, ok := d.approveFromStoredGrant(sess, externalize); ok {
			externalize = approved
		}
	}
	if err != nil || externalize.Decision != "allowed" {
		reason := "externalization_policy_denied"
		if err != nil {
			reason = "externalization_policy_error"
		} else if externalize.Decision == "requires_approval" {
			_, _ = d.kern.Deny(sess.SessionID, externalize.DecisionID, "system", "HMS recall has no stored MemoryExternalize grant")
		}
		provider.markPolicyDenied(reason)
		d.recordMemoryRecall(sess, task, "degraded", reason, 0)
		return ""
	}
	if err := d.recordChecked(sess.SessionID, "MemoryRecallRequested", task.TaskID, "go", map[string]any{"provider": "hms", "endpoint_host": resource, "query_sha256": hashMemoryQuery(task.UserPrompt), "status": "authorized"}, externalize.DecisionID); err != nil {
		provider.markPolicyDenied("audit_unavailable")
		return ""
	}
	packet, err := provider.Recall(ctx, memoryScopeFromSession(sess), task.UserPrompt)
	if err != nil {
		d.recordMemoryRecall(sess, task, "degraded", provider.Health().LastReason, 0)
		return ""
	}
	status := "injected"
	if provider.mode == memoryProviderHMSShadow {
		status = "shadow"
	}
	d.recordMemoryRecall(sess, task, status, "", len(packet.Evidence))
	if provider.mode == memoryProviderHMSShadow {
		return ""
	}
	evidence := renderHMSEvidence(packet)
	return strings.TrimSpace(evidence)
}

func (d *Daemon) recordMemoryRecall(sess *sessionstore.Session, task *scheduler.Task, status, reason string, count int) {
	d.record(sess.SessionID, "MemoryRecalled", task.TaskID, "go", map[string]any{
		"provider": "hms", "mode": d.memoryHMS.mode, "status": status,
		"reason": reason, "evidence_count": count, "adapter_version": hmsAdapterVersion,
	}, "")
}
