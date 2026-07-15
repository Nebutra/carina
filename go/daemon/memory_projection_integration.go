package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/kernel"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func (d *Daemon) memoryProjectionStatus() map[string]any {
	if d.memoryProjection == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{"enabled": true, "provider": "hms", "status": d.memoryProjection.Status()}
}

func (d *Daemon) handleMemoryProjectionAuthorize(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	if d.memoryProjection == nil {
		return nil, fmt.Errorf("HMS memory projection is disabled")
	}
	scope := memoryScopeFromSession(sess)
	if err := d.memoryProjection.FailedToBlocked(scope); err != nil {
		return nil, err
	}
	blocked := d.memoryProjection.Blocked(scope)
	results := make([]*memoryProjectionWriteResult, 0, len(blocked))
	for _, intent := range blocked {
		if intent.DecisionID != "" {
			d.mu.Lock()
			_, live := d.pendingMemProjections[intent.DecisionID]
			d.mu.Unlock()
			if live {
				results = append(results, &memoryProjectionWriteResult{Enabled: true, Status: projectionBlocked, DocumentID: intent.DocumentID, Revision: intent.Revision, DecisionID: intent.DecisionID, Decision: "requires_approval"})
				continue
			}
			_ = d.memoryProjection.SetBlockedReason(intent.DocumentID, intent.Generation, "authorization_required")
			intent.DecisionID = ""
		}
		results = append(results, d.authorizeMemoryProjection(sess, intent, ""))
	}
	return map[string]any{"projections": results}, nil
}

const (
	projectionApprovalNetwork     = "network"
	projectionApprovalExternalize = "externalize"
)

func renderMemoryProjectionState(entries []string) (string, bool) {
	if len(entries) == 0 {
		return "", true
	}
	raw, _ := json.Marshal(struct {
		Version int      `json:"version"`
		Entries []string `json:"entries"`
	}{Version: 1, Entries: entries})
	return string(raw), false
}

func (d *Daemon) prepareMemoryProjection(sess *sessionstore.Session, scope memoryScope, target string) (*memoryProjectionIntent, error) {
	if d.memoryProjection == nil || d.memoryHMS == nil {
		return nil, nil
	}
	intent, err := d.memoryProjection.MarkDirty(scope, target, d.memoryHMS.bankID(scope, target), sess.SessionID)
	if err != nil {
		return nil, err
	}
	return &intent, nil
}

func (d *Daemon) finishMemoryProjection(sess *sessionstore.Session, dirty *memoryProjectionIntent) *memoryProjectionWriteResult {
	if dirty == nil || d.memoryProjection == nil {
		return nil
	}
	state, err := d.memory.list(dirty.Scope, dirty.Target)
	if err != nil {
		return &memoryProjectionWriteResult{Enabled: true, Status: projectionDirty, DocumentID: dirty.DocumentID}
	}
	content, tombstone := renderMemoryProjectionState(state.Entries)
	intent, err := d.memoryProjection.SetDesired(dirty.DocumentID, dirty.Generation, content, tombstone)
	if err != nil {
		return &memoryProjectionWriteResult{Enabled: true, Status: projectionDirty, DocumentID: dirty.DocumentID}
	}
	return d.authorizeMemoryProjection(sess, intent, "")
}

func (d *Daemon) authorizeMemoryProjection(sess *sessionstore.Session, intent memoryProjectionIntent, taskID string) *memoryProjectionWriteResult {
	intent.SessionID = sess.SessionID
	_ = d.memoryProjection.RebindSession(intent.DocumentID, intent.Generation, sess.SessionID)
	networkResource := strings.ToLower(d.memoryHMS.endpoint.Hostname())
	decision, err := d.requestProjectionCapability(sess, "NetworkAccess", networkResource, taskID)
	if err != nil {
		return d.blockProjection(intent, "network_policy_error", nil)
	}
	if decision.Decision != "allowed" {
		return d.pendingOrBlockedProjection(intent, decision, projectionApprovalNetwork)
	}
	if err := d.memoryProjection.SetNetworkDecision(intent.DocumentID, intent.Generation, decision.DecisionID); err != nil {
		return d.blockProjection(intent, "network_decision_persist_failed", decision)
	}
	intent.NetworkDecisionID = decision.DecisionID
	return d.authorizeMemoryProjectionAfterNetwork(sess, intent, taskID)
}

func (d *Daemon) authorizeMemoryProjectionAfterNetwork(sess *sessionstore.Session, intent memoryProjectionIntent, taskID string) *memoryProjectionWriteResult {
	intent.SessionID = sess.SessionID
	_ = d.memoryProjection.RebindSession(intent.DocumentID, intent.Generation, sess.SessionID)
	action := "retain"
	if intent.Tombstone {
		action = "delete"
	}
	resource := fmt.Sprintf("provider=hms bank=%s document=%s target=%s action=%s revision=%s", intent.BankID, intent.DocumentID, intent.Target, action, intent.Revision)
	decision, err := d.requestProjectionCapability(sess, "MemoryExternalize", resource, taskID)
	if err != nil {
		return d.blockProjection(intent, "externalize_policy_error", nil)
	}
	if decision.Decision != "allowed" {
		return d.pendingOrBlockedProjection(intent, decision, projectionApprovalExternalize)
	}
	if err := d.memoryProjection.Authorize(intent.DocumentID, intent.Generation, decision.DecisionID); err != nil {
		return d.blockProjection(intent, "outbox_authorize_failed", decision)
	}
	d.recordMemoryProjection(intent, "pending", "", decision.DecisionID)
	return &memoryProjectionWriteResult{Enabled: true, Status: projectionPending, DocumentID: intent.DocumentID, Revision: intent.Revision, DecisionID: decision.DecisionID, Decision: decision.Decision}
}

func (d *Daemon) requestProjectionCapability(sess *sessionstore.Session, capability, resource, taskID string) (*kernel.Decision, error) {
	decision, err := d.kern.Request(sess.SessionID, capability, resource, taskID)
	if err != nil {
		return nil, err
	}
	if decision.Decision == "requires_approval" {
		if approved, ok := d.approveFromStoredGrant(sess, decision); ok {
			decision = approved
		}
	}
	return decision, nil
}

func (d *Daemon) pendingOrBlockedProjection(intent memoryProjectionIntent, decision *kernel.Decision, stage string) *memoryProjectionWriteResult {
	if decision.Decision == "requires_approval" {
		_ = d.memoryProjection.SetDecision(intent.DocumentID, intent.Generation, decision.DecisionID)
		d.mu.Lock()
		d.pendingMemProjections[decision.DecisionID] = pendingMemoryProjection{sessionID: intent.SessionID, documentID: intent.DocumentID, generation: intent.Generation, stage: stage}
		d.mu.Unlock()
		d.recordMemoryProjection(intent, projectionBlocked, "authorization_required", decision.DecisionID)
		return &memoryProjectionWriteResult{Enabled: true, Status: projectionBlocked, DocumentID: intent.DocumentID, Revision: intent.Revision, DecisionID: decision.DecisionID, Decision: decision.Decision}
	}
	return d.blockProjection(intent, "authorization_denied", decision)
}

func (d *Daemon) blockProjection(intent memoryProjectionIntent, reason string, decision *kernel.Decision) *memoryProjectionWriteResult {
	_ = d.memoryProjection.SetBlockedReason(intent.DocumentID, intent.Generation, reason)
	decisionID, verdict := "", "denied"
	if decision != nil {
		decisionID, verdict = decision.DecisionID, decision.Decision
	}
	d.recordMemoryProjection(intent, projectionBlocked, reason, decisionID)
	return &memoryProjectionWriteResult{Enabled: true, Status: projectionBlocked, DocumentID: intent.DocumentID, Revision: intent.Revision, DecisionID: decisionID, Decision: verdict}
}

func (d *Daemon) recordMemoryProjection(intent memoryProjectionIntent, status, reason, decisionID string) {
	d.record(intent.SessionID, "MemoryProjectionChanged", "", "go", map[string]any{
		"provider": "hms", "status": status, "reason": reason, "target": intent.Target,
		"document_id": intent.DocumentID, "revision": intent.Revision, "generation": intent.Generation,
	}, decisionID)
}

type auditedProjectionExecutor struct {
	d    *Daemon
	next memoryProjectionExecutor
}

func (e auditedProjectionExecutor) Put(ctx context.Context, intent memoryProjectionIntent) error {
	return e.execute(ctx, intent, false)
}
func (e auditedProjectionExecutor) Delete(ctx context.Context, intent memoryProjectionIntent) error {
	return e.execute(ctx, intent, true)
}
func (e auditedProjectionExecutor) execute(ctx context.Context, intent memoryProjectionIntent, tombstone bool) error {
	if intent.NetworkDecisionID == "" || intent.AuthorizationDecisionID == "" {
		return permanentMemoryProjectionError(fmt.Errorf("projection authorization proof is incomplete"))
	}
	var err error
	if tombstone {
		err = e.next.Delete(ctx, intent)
	} else {
		err = e.next.Put(ctx, intent)
	}
	status, reason := "attempt_succeeded", ""
	if err != nil {
		status, reason = "attempt_failed", "retryable_or_permanent"
	}
	if auditErr := e.d.recordChecked(intent.SessionID, "MemoryProjected", "", "go", map[string]any{
		"provider": "hms", "status": status, "reason": reason, "target": intent.Target,
		"document_id": intent.DocumentID, "revision": intent.Revision, "generation": intent.Generation,
		"network_decision_id": intent.NetworkDecisionID,
	}, intent.AuthorizationDecisionID); auditErr != nil {
		return permanentMemoryProjectionError(fmt.Errorf("projection side effect audit failed"))
	}
	return err
}

func (d *Daemon) runMemoryProjectionLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-d.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	interval := d.memoryProjectionPoll
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			processed, _ := d.memoryProjection.ProcessOne(ctx, d.memoryProjectionExecutor)
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) reconcileDirtyMemoryProjections() {
	if d.memoryProjection == nil {
		return
	}
	for _, dirty := range d.memoryProjection.Dirty() {
		state, err := d.memory.list(dirty.Scope, dirty.Target)
		if err != nil {
			continue
		}
		content, tombstone := renderMemoryProjectionState(state.Entries)
		_, _ = d.memoryProjection.SetDesired(dirty.DocumentID, dirty.Generation, content, tombstone)
	}
}
