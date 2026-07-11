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
			detail, _ := d.workflowRuns.Detail(run.ID)
			if detail.Run.Status != workflowui.Stopped {
				_, _ = d.workflowRuns.Transition(run.ID, workflowui.Failed)
			}
			return
		}
		for _, st := range spec.Steps {
			_, _ = d.workflowRuns.UpdateStep(run.ID, workflowui.Step{ID: st.ID, Status: workflowui.Completed, Output: outputs[st.ID]})
		}
		_, _ = d.workflowRuns.Transition(run.ID, workflowui.Completed)
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
	return d.workflowTransition(p, workflowui.Running)
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
	run, err := d.workflowRuns.Restart(p.RunID, sessionstore.NewID("wf"))
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
		Secret             string   `json:"secret"`
		Sessions           []string `json:"sessions"`
		Kinds              []string `json:"kinds"`
		CanRelayPermission bool     `json:"can_relay_permission"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if err := d.channels.Register(channels.Sender{ID: p.ID, Secret: []byte(p.Secret), Sessions: p.Sessions, Kinds: p.Kinds, CanRelayPermission: p.CanRelayPermission}); err != nil {
		return nil, err
	}
	return map[string]any{"registered": true, "sender_id": p.ID}, nil
}
func (d *Daemon) handleChannelSenderList(json.RawMessage) (any, error) {
	return d.channels.Senders(), nil
}
func (d *Daemon) handleChannelEventInject(params json.RawMessage) (any, error) {
	var p struct {
		Event     channels.Event `json:"event"`
		Signature string         `json:"signature"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	receipt, err := d.channels.Ingest(p.Event, p.Signature)
	if err != nil {
		return nil, err
	}
	if receipt.Duplicate {
		return receipt, nil
	}
	d.events.Publish(p.Event.SessionID, map[string]any{"type": "ExternalEvent", "session_id": p.Event.SessionID, "timestamp": time.Now().UTC(), "payload": map[string]any{"event_id": p.Event.ID, "sender_id": p.Event.SenderID, "kind": p.Event.Kind, "data": p.Event.Payload}})
	if p.Event.PermissionDecisionID != "" && p.Event.PermissionAllow != nil {
		raw, _ := json.Marshal(map[string]any{"decision_id": p.Event.PermissionDecisionID, "allow": *p.Event.PermissionAllow, "approver": "channel:" + p.Event.SenderID, "scope": "once"})
		if _, err := d.handleApprovalResolve(raw); err != nil {
			return nil, fmt.Errorf("channel permission relay: %w", err)
		}
	}
	return receipt, nil
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
	return map[string]any{"enabled": d.telemetry.Enabled(), "schema_url": "https://opentelemetry.io/schemas/1.27.0"}, nil
}
