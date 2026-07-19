package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/continuity"
	"github.com/Nebutra/carina/go/scheduler"
)

type recoveryToolCall struct {
	started  bool
	terminal bool
	effect   continuity.EffectContract
}

func (d *Daemon) reconcileInterruptedTask(task *scheduler.Task) {
	cp := d.runs.loadCheckpoint(task.TaskID)
	_, sessionOK := d.store.Get(task.SessionID)
	proofs := map[string]bool{
		"checkpoint":    cp != nil && cp.Transcript != nil,
		"effect_replay": false, "workspace_anchor": false, "external_effects": false,
	}
	reasons := []string{}
	if !sessionOK {
		reasons = append(reasons, "session is unavailable")
	}
	if !proofs["checkpoint"] {
		reasons = append(reasons, "no durable checkpoint")
	}
	effectSafe, externalSafe, effectReason := d.recoveryEffectProof(task)
	proofs["effect_replay"], proofs["external_effects"] = effectSafe, externalSafe
	if !effectSafe || !externalSafe {
		reasons = append(reasons, effectReason)
	}
	if cp != nil {
		proofs["workspace_anchor"], effectReason = verifyWorkspaceAnchor(cp.WorkspaceAnchor)
		if !proofs["workspace_anchor"] {
			reasons = append(reasons, effectReason)
		}
	}
	allPassed := sessionOK
	for _, passed := range proofs {
		allPassed = allPassed && passed
	}
	disposition := continuity.RecoveryReviewRequired
	if allPassed {
		disposition = continuity.RecoveryResumeCheckpoint
		reasons = []string{"all automatic recovery proofs passed"}
	}
	kind := continuity.InterruptionRuntimeLost
	if d.runtimeLease.previousGraceful {
		kind = continuity.InterruptionGracefulShutdown
	}
	record := continuity.InterruptionRecord{
		Kind: kind, Actor: "runtime", ObservedAt: time.Now().UTC(), RuntimeEpoch: d.runtimeLease.state.Epoch - 1,
		TaskID: task.TaskID, Certainty: continuity.CertaintyInferred, Retryable: allPassed,
	}
	record.BillingUncertain = d.hasUnsettledModelRequest(task)
	if cp != nil {
		record.CheckpointID = runCheckpointID(task.TaskID, cp)
	}
	decision := continuity.RecoveryDecision{
		Disposition: disposition, Reason: strings.Join(reasons, "; "), CheckpointID: record.CheckpointID,
		ExpectedTaskRevision: task.Revision, Proofs: proofs,
	}
	interrupted, err := d.sched.Interrupt(task.TaskID, record, decision)
	if err != nil {
		return
	}
	d.sched.SetResult(task.TaskID, "interrupted: "+decision.Reason, task.AppliedPatches)
	d.persistRun(task.TaskID)
	d.record(task.SessionID, "TaskInterrupted", task.TaskID, "go", map[string]any{
		"kind": kind, "certainty": record.Certainty, "retryable": allPassed,
		"runtime_epoch": record.RuntimeEpoch, "checkpoint_id": record.CheckpointID, "billing_uncertain": record.BillingUncertain,
	}, "")
	eventType := "TaskRecoveryBlocked"
	if allPassed {
		eventType = "TaskRecoveryPlanned"
	}
	d.record(task.SessionID, eventType, task.TaskID, "go", map[string]any{
		"disposition": disposition, "recovery_generation": interrupted.Continuity.RecoveryGeneration,
		"proofs": proofs, "reason": decision.Reason,
	}, "")
	if !allPassed {
		return
	}
	d.startPlannedRecovery(interrupted)
}

func (d *Daemon) startPlannedRecovery(task *scheduler.Task) {
	if d.reasoner == nil || task == nil || task.Status != "interrupted" || task.Continuity.Recovery.Disposition != continuity.RecoveryResumeCheckpoint {
		return
	}
	sess, ok := d.store.Get(task.SessionID)
	if !ok {
		return
	}
	cp := d.runs.loadCheckpoint(task.TaskID)
	if cp == nil || runCheckpointID(task.TaskID, cp) != task.Continuity.Recovery.CheckpointID {
		return
	}
	current, _ := d.sched.Get(task.TaskID)
	claimed, err := d.sched.AcquireExecution(task.TaskID, current.Revision, "local", d.runtimeLease.state.InstanceID, d.runtimeLease.state.Epoch, time.Time{})
	if err != nil {
		return
	}
	d.persistRun(task.TaskID)
	d.record(task.SessionID, "TaskRecoveryStarted", task.TaskID, "go", map[string]any{
		"recovery_generation":  claimed.Continuity.RecoveryGeneration,
		"execution_generation": claimed.Continuity.Execution.LeaseGeneration, "checkpoint_id": task.Continuity.Recovery.CheckpointID,
	}, "")
	go d.resumeTaskGuarded(sess, claimed, cp)
}

func (d *Daemon) recoveryEffectProof(task *scheduler.Task) (bool, bool, string) {
	raw, err := d.kern.ReadEvents(task.SessionID)
	if err != nil {
		return false, false, "audit events unavailable"
	}
	var events []itemAuditEvent
	if json.Unmarshal(raw, &events) != nil {
		return false, false, "audit events are corrupt"
	}
	calls := map[string]*recoveryToolCall{}
	for _, event := range events {
		if event.TaskID != task.TaskID {
			continue
		}
		callID, _ := event.Payload["call_id"].(string)
		if callID == "" {
			continue
		}
		call := calls[callID]
		if call == nil {
			call = &recoveryToolCall{}
			calls[callID] = call
		}
		switch event.Type {
		case "ToolCallRequested":
			effectRaw, _ := json.Marshal(event.Payload["effect"])
			_ = json.Unmarshal(effectRaw, &call.effect)
		case "ToolCallStarted":
			call.started = true
		case "ToolCallCompleted", "ToolCallFailed", "ToolCallDenied", "ToolCallCancelled":
			call.terminal = true
		}
	}
	for callID, call := range calls {
		if !call.started || call.terminal {
			continue
		}
		if call.effect.Authority != "carina-runtime-v1" || !call.effect.ReplaySafe {
			return false, false, fmt.Sprintf("unsettled tool call %s has unsafe or legacy effect contract", callID)
		}
		if call.effect.Class == continuity.EffectWorkspaceTransactional {
			return false, false, fmt.Sprintf("unsettled workspace transaction %s requires durable effect reconciliation", callID)
		}
		if call.effect.Class == continuity.EffectIdempotentExternal && call.effect.IdempotencyKey == "" {
			return true, false, fmt.Sprintf("unsettled external tool call %s has no idempotency key", callID)
		}
	}
	return true, true, "all started tool calls are settled or replay-safe"
}

func (d *Daemon) hasUnsettledModelRequest(task *scheduler.Task) bool {
	raw, err := d.kern.ReadEvents(task.SessionID)
	if err != nil {
		return true
	}
	var events []itemAuditEvent
	if json.Unmarshal(raw, &events) != nil {
		return true
	}
	pending := 0
	for _, event := range events {
		if event.TaskID != task.TaskID {
			continue
		}
		switch event.Type {
		case "ModelRequested":
			pending++
		case "ModelResponded", "RoutingOutcome":
			if pending > 0 {
				pending--
			}
		}
	}
	return pending > 0
}
