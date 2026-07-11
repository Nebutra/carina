package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Nebutra/carina/go/channels"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/workflowui"
)

func (d *Daemon) handleWorkflowRun(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Workflow  string `json:"workflow"`
		Input     string `json:"input"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	spec := loadWorkflowSpecs(sess.WorkspaceRoot)[p.Workflow]
	if spec == nil {
		return nil, fmt.Errorf("unknown workflow %q", p.Workflow)
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	runID := sessionstore.NewID("wf")
	steps := make([]workflowui.Step, 0, len(spec.Steps))
	for _, st := range spec.Steps {
		steps = append(steps, workflowui.Step{ID: st.ID})
	}
	run, err := d.workflowRuns.Create(workflowui.Run{ID: runID, Workflow: p.Workflow, SessionID: p.SessionID, Input: p.Input, Steps: steps})
	if err != nil {
		return nil, err
	}
	d.startWorkflowControlRun(sess, spec, run)
	return run, nil
}

func (d *Daemon) startWorkflowControlRun(sess *sessionstore.Session, spec *WorkflowSpec, run workflowui.Run) {
	d.taskWG.Add(1)
	go func() {
		defer d.taskWG.Done()
		_, err := d.workflowRuns.Transition(run.ID, workflowui.Running)
		if err != nil {
			return
		}
		task := &scheduler.Task{TaskID: sessionstore.NewID("task"), SessionID: sess.SessionID, WorkspaceID: sess.WorkspaceID, Status: "running", UserPrompt: run.Input, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		outputs, runErr := d.runWorkflow(sess, task, spec, run.Input, run.ID)
		if runErr != nil {
			detail, detailErr := d.workflowRuns.Detail(run.ID)
			if detailErr == nil && detail.Run.Status != workflowui.Stopped && detail.Run.Status != workflowui.Interrupted {
				if _, e := d.workflowRuns.Transition(run.ID, workflowui.Failed); e != nil {
					_, _ = d.workflowRuns.MarkInterrupted(run.ID, "execution failed and failure status could not be persisted: "+e.Error())
				}
			}
			return
		}
		for _, st := range spec.Steps {
			if _, err := d.workflowRuns.UpdateStep(run.ID, workflowui.Step{ID: st.ID, Status: workflowui.Completed, Output: outputs[st.ID]}); err != nil {
				_, _ = d.workflowRuns.MarkInterrupted(run.ID, "result completed but final step persistence failed: "+err.Error())
				return
			}
		}
		if _, err := d.workflowRuns.Transition(run.ID, workflowui.Completed); err != nil {
			_, _ = d.workflowRuns.MarkInterrupted(run.ID, "result completed but terminal status persistence failed: "+err.Error())
		}
	}()
}

func (d *Daemon) handleWorkflowList(json.RawMessage) (any, error) { return d.workflowRuns.List(), nil }
func (d *Daemon) handleWorkflowDetail(params json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return d.workflowRuns.Detail(p.RunID)
}
func (d *Daemon) workflowTransition(params json.RawMessage, status workflowui.Status) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return d.workflowRuns.Transition(p.RunID, status)
}
func (d *Daemon) handleWorkflowPause(p json.RawMessage) (any, error) {
	return d.workflowTransition(p, workflowui.Paused)
}
func (d *Daemon) handleWorkflowResume(p json.RawMessage) (any, error) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(p, &req); err != nil {
		return nil, err
	}
	detail, err := d.workflowRuns.Detail(req.RunID)
	if err != nil {
		return nil, err
	}
	if detail.Run.Status != workflowui.Interrupted {
		return d.workflowRuns.Transition(req.RunID, workflowui.Running)
	}
	if !detail.Run.Resumable {
		return nil, fmt.Errorf("workflow %s is blocked and requires manual reconciliation: %s", req.RunID, detail.Run.InterruptionReason)
	}
	sess, ok := d.store.Get(detail.Run.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", detail.Run.SessionID)
	}
	spec := loadWorkflowSpecs(sess.WorkspaceRoot)[detail.Run.Workflow]
	if spec == nil {
		return nil, fmt.Errorf("unknown workflow %q", detail.Run.Workflow)
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	run, err := d.workflowRuns.Transition(req.RunID, workflowui.Queued)
	if err != nil {
		return nil, err
	}
	d.startWorkflowControlRun(sess, spec, run)
	return run, nil
}
func (d *Daemon) handleWorkflowStop(p json.RawMessage) (any, error) {
	return d.workflowTransition(p, workflowui.Stopped)
}
func (d *Daemon) handleWorkflowRestart(params json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	old, err := d.workflowRuns.Detail(p.RunID)
	if err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(old.Run.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", old.Run.SessionID)
	}
	spec := loadWorkflowSpecs(sess.WorkspaceRoot)[old.Run.Workflow]
	if spec == nil {
		return nil, fmt.Errorf("unknown workflow %q", old.Run.Workflow)
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	run, err := d.workflowRuns.Restart(p.RunID, sessionstore.NewID("wf"))
	if err != nil {
		return nil, err
	}
	d.startWorkflowControlRun(sess, spec, run)
	return run, nil
}
func (d *Daemon) handleWorkflowSave(params json.RawMessage) (any, error) {
	var p struct {
		RunID string `json:"run_id"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path, err := d.workflowRuns.SaveCommand(p.RunID, filepath.Join(home, ".carina", "commands"), p.Name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path}, nil
}

func (d *Daemon) handleChannelSenderRegister(params json.RawMessage) (any, error) {
	var p struct {
		ID                 string   `json:"id"`
		SecretEnv          string   `json:"secret_env"`
		Sessions           []string `json:"sessions"`
		Kinds              []string `json:"kinds"`
		CanRelayPermission bool     `json:"can_relay_permission"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.SecretEnv == "" {
		return nil, fmt.Errorf("secret_env is required")
	}
	if err := d.channels.Register(channels.Sender{ID: p.ID, SecretRef: "env:" + p.SecretEnv, Sessions: p.Sessions, Kinds: p.Kinds, CanRelayPermission: p.CanRelayPermission}); err != nil {
		return nil, err
	}
	return map[string]any{"registered": true, "sender_id": p.ID}, nil
}
func (d *Daemon) handleChannelSenderList(json.RawMessage) (any, error) {
	return d.channels.Senders(), nil
}
func (d *Daemon) handleChannelEventPending(json.RawMessage) (any, error) {
	return map[string]any{"incidents": d.channels.Incidents()}, nil
}
func (d *Daemon) handleChannelEventReconcile(params json.RawMessage) (any, error) {
	var p struct {
		SenderID  string `json:"sender_id"`
		EventID   string `json:"event_id"`
		Outcome   string `json:"outcome"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if !p.Confirmed {
		return nil, fmt.Errorf("channel reconciliation requires confirmed=true")
	}
	var executed bool
	switch p.Outcome {
	case "executed":
		executed = true
	case "not_executed":
		executed = false
	default:
		return nil, fmt.Errorf("outcome must be executed or not_executed")
	}
	if err := d.channels.ReconcileConfirmed(p.SenderID, p.EventID, executed); err != nil {
		return nil, err
	}
	return map[string]any{"reconciled": true, "sender_id": p.SenderID, "event_id": p.EventID, "outcome": p.Outcome}, nil
}
func (d *Daemon) handleChannelEventInject(params json.RawMessage) (any, error) {
	var p struct {
		Event     channels.Event `json:"event"`
		Signature string         `json:"signature"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	reservation, err := d.channels.Reserve(p.Event, p.Signature)
	if err != nil {
		return nil, err
	}
	receipt := reservation.Receipt
	if receipt.Duplicate {
		return receipt, nil
	}
	task := d.activeChannelTask(p.Event.SessionID)
	if task == nil && p.Event.PermissionDecisionID == "" {
		d.channels.Abort(reservation)
		return nil, fmt.Errorf("channel event has no active task in session %s", p.Event.SessionID)
	}
	committed := false
	defer func() {
		if !committed {
			d.channels.Abort(reservation)
		}
	}()
	if err := d.channels.MarkEffectStarted(reservation); err != nil {
		return nil, fmt.Errorf("channel effect start journal: %w", err)
	}
	if p.Event.PermissionDecisionID != "" && p.Event.PermissionAllow != nil {
		raw, _ := json.Marshal(map[string]any{"decision_id": p.Event.PermissionDecisionID, "allow": *p.Event.PermissionAllow, "approver": "channel:" + p.Event.SenderID, "scope": "once"})
		if _, err := d.handleApprovalResolve(raw); err != nil {
			return nil, fmt.Errorf("channel permission relay: %w", err)
		}
	}
	taskID := ""
	if task != nil {
		taskID = task.TaskID
	}
	payload := map[string]any{"status": "external_event", "event_id": p.Event.ID, "sender_id": p.Event.SenderID, "kind": p.Event.Kind, "data": p.Event.Payload}
	if err := d.kern.RecordEvent(p.Event.SessionID, "TaskCreated", taskID, "channel", payload, p.Event.PermissionDecisionID); err != nil {
		return nil, fmt.Errorf("channel audit append: %w", err)
	}
	d.events.Publish(p.Event.SessionID, map[string]any{"type": "ExternalEvent", "session_id": p.Event.SessionID, "task_id": taskID, "timestamp": time.Now().UTC(), "payload": payload})
	if task != nil {
		data, _ := json.Marshal(p.Event.Payload)
		d.steer(task.TaskID, fmt.Sprintf("CHANNEL EVENT %s from %s (event %s): %s", p.Event.Kind, p.Event.SenderID, p.Event.ID, data))
	}
	if err := d.channels.MarkEffectApplied(reservation); err != nil {
		return nil, fmt.Errorf("channel side effect applied but journal update failed: %w", err)
	}
	if err := d.channels.Commit(reservation); err != nil {
		return nil, err
	}
	committed = true
	return receipt, nil
}

func (d *Daemon) activeChannelTask(sessionID string) *scheduler.Task {
	var selected *scheduler.Task
	for _, task := range d.sched.List() {
		if task.SessionID != sessionID {
			continue
		}
		switch task.Status {
		case "queued", "running", "waiting_approval", "needs_input":
		default:
			continue
		}
		if selected == nil || task.UpdatedAt.After(selected.UpdatedAt) {
			selected = task
		}
	}
	return selected
}

func (d *Daemon) handleExtensionInstall(params json.RawMessage) (any, error) {
	var p struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return d.extensions.Install(p.Source)
}
func (d *Daemon) handleExtensionList(json.RawMessage) (any, error) {
	return d.extensions.Inventory(), nil
}
func (d *Daemon) extensionEnabled(params json.RawMessage, on bool) (any, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return d.extensions.SetEnabled(p.Name, on)
}
func (d *Daemon) handleExtensionEnable(p json.RawMessage) (any, error) {
	return d.extensionEnabled(p, true)
}
func (d *Daemon) handleExtensionDisable(p json.RawMessage) (any, error) {
	return d.extensionEnabled(p, false)
}
func (d *Daemon) handleExtensionUpdate(params json.RawMessage) (any, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return d.extensions.Update(p.Name)
}
func (d *Daemon) handleExtensionSafeMode(params json.RawMessage) (any, error) {
	var p struct {
		On bool `json:"on"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if err := d.extensions.SetSafeMode(p.On); err != nil {
		return nil, err
	}
	return d.extensions.Inventory(), nil
}
func (d *Daemon) handleTelemetryStatus(json.RawMessage) (any, error) {
	return map[string]any{"enabled": d.telemetry.Enabled(), "format": "carina-telemetry-json-v1", "otlp": false}, nil
}
