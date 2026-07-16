package daemon

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
)

func (d *Daemon) handleScheduleCreate(params json.RawMessage) (any, error) {
	var p struct {
		SessionID         string `json:"session_id"`
		Prompt            string `json:"prompt"`
		Kind              string `json:"kind"`
		Expression        string `json:"expression"`
		Model             string `json:"model"`
		ReasoningEffort   string `json:"reasoning_effort"`
		Agent             string `json:"agent"`
		Mode              string `json:"mode"`
		ConcurrencyPolicy string `json:"concurrency_policy"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	if sess.Status != "active" {
		return nil, fmt.Errorf("session %s is %s, not active", p.SessionID, sess.Status)
	}
	model := p.Model
	if model == "" {
		model = sess.NextModel
	}
	effort := p.ReasoningEffort
	if effort == "" {
		effort = sess.NextReasoningEffort
	}
	mode := p.Mode
	if mode == "" {
		mode = "background"
	}
	row, err := d.schedules.CreateWithEnvelope(scheduler.Schedule{SessionID: p.SessionID, Prompt: p.Prompt, Kind: p.Kind, Expression: p.Expression, Model: model, ReasoningEffort: effort, Agent: p.Agent, Mode: mode, PermissionProfile: sess.PermissionProfile, ApprovalMode: sess.ApprovalMode, ConcurrencyPolicy: p.ConcurrencyPolicy}, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	d.record(p.SessionID, "ScheduleChanged", "", "go", map[string]any{"action": "created", "schedule_id": row.ScheduleID, "schedule": row}, "")
	return row, nil
}

func (d *Daemon) handleScheduleList(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	rows := []*scheduler.Schedule{}
	for _, row := range d.schedules.List() {
		if row.SessionID == p.SessionID {
			rows = append(rows, row)
		}
	}
	return map[string]any{"schedules": rows}, nil
}

func (d *Daemon) handleSchedulePause(params json.RawMessage) (any, error) {
	id, sessionID, err := scheduleID(params)
	if err != nil {
		return nil, err
	}
	if err := d.checkScheduleOwner(id, sessionID); err != nil {
		return nil, err
	}
	row, err := d.schedules.SetEnabled(id, false, time.Now().UTC())
	if err == nil {
		d.record(row.SessionID, "ScheduleChanged", "", "go", map[string]any{"action": "paused", "schedule_id": id}, "")
	}
	return row, err
}

func (d *Daemon) handleScheduleResume(params json.RawMessage) (any, error) {
	id, sessionID, err := scheduleID(params)
	if err != nil {
		return nil, err
	}
	if err := d.checkScheduleOwner(id, sessionID); err != nil {
		return nil, err
	}
	row, err := d.schedules.SetEnabled(id, true, time.Now().UTC())
	if err == nil {
		d.record(row.SessionID, "ScheduleChanged", "", "go", map[string]any{"action": "resumed", "schedule_id": id}, "")
	}
	return row, err
}

func (d *Daemon) handleScheduleDelete(params json.RawMessage) (any, error) {
	id, sessionID, err := scheduleID(params)
	if err != nil {
		return nil, err
	}
	if err := d.checkScheduleOwner(id, sessionID); err != nil {
		return nil, err
	}
	row, err := d.schedules.Delete(id)
	if err != nil {
		return nil, err
	}
	d.record(row.SessionID, "ScheduleChanged", "", "go", map[string]any{"action": "deleted", "schedule_id": id}, "")
	return map[string]any{"deleted": true, "schedule_id": id}, nil
}

func scheduleID(params json.RawMessage) (string, string, error) {
	var p struct {
		ScheduleID string `json:"schedule_id"`
		SessionID  string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", "", fmt.Errorf("invalid params: %w", err)
	}
	if p.ScheduleID == "" {
		return "", "", fmt.Errorf("schedule_id is required")
	}
	if p.SessionID == "" {
		return "", "", fmt.Errorf("session_id is required")
	}
	return p.ScheduleID, p.SessionID, nil
}

func (d *Daemon) checkScheduleOwner(id, sessionID string) error {
	for _, row := range d.schedules.List() {
		if row.ScheduleID == id {
			if row.SessionID != sessionID {
				return fmt.Errorf("schedule %s does not belong to session %s", id, sessionID)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown schedule %s", id)
}

func (d *Daemon) runScheduleLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case now := <-ticker.C:
			for _, row := range d.schedules.ClaimDue(now.UTC()) {
				sess, ok := d.store.Get(row.SessionID)
				if !ok || sess.Status != "active" || (row.PermissionProfile != "" && (sess.PermissionProfile != row.PermissionProfile || sess.ApprovalMode != row.ApprovalMode)) {
					d.schedules.RetryClaim(row.ScheduleID, now.UTC())
					d.record(row.SessionID, "ScheduleTriggered", "", "go", map[string]any{"schedule_id": row.ScheduleID, "status": "failed", "error": "session or frozen permission envelope changed"}, "")
					continue
				}
				if d.resolveScheduleOverlap(row, now.UTC()) {
					continue
				}
				result, err := d.handleTaskSubmit(mustScheduleParams(row))
				if err != nil {
					d.schedules.RetryClaim(row.ScheduleID, now.UTC())
					d.record(row.SessionID, "ScheduleTriggered", "", "go", map[string]any{"schedule_id": row.ScheduleID, "status": "failed", "error": err.Error()}, "")
					continue
				}
				taskID := ""
				if task, ok := result.(*scheduler.Task); ok {
					taskID = task.TaskID
				} else if raw, err := json.Marshal(result); err == nil {
					var task struct {
						TaskID string `json:"task_id"`
					}
					_ = json.Unmarshal(raw, &task)
					taskID = task.TaskID
				}
				d.schedules.MarkTask(row.ScheduleID, taskID)
				d.record(row.SessionID, "ScheduleTriggered", taskID, "go", map[string]any{"schedule_id": row.ScheduleID, "status": "submitted"}, "")
			}
		}
	}
}

func (d *Daemon) resolveScheduleOverlap(row *scheduler.Schedule, now time.Time) bool {
	if row.ConcurrencyPolicy == "allow" || row.LastTaskID == "" {
		return false
	}
	previous, exists := d.sched.Get(row.LastTaskID)
	if !exists || (previous.Status != "queued" && previous.Status != "running" && previous.Status != "waiting_approval") {
		return false
	}
	switch row.ConcurrencyPolicy {
	case "queue":
		d.schedules.QueueClaim(row.ScheduleID, now)
		d.record(row.SessionID, "ScheduleTriggered", row.LastTaskID, "go", map[string]any{"schedule_id": row.ScheduleID, "status": "queued", "reason": "previous_task_running"}, "")
		return true
	case "replace":
		if _, err := d.handleTaskCancel(mustRaw(map[string]any{"task_id": row.LastTaskID})); err != nil {
			d.schedules.QueueClaim(row.ScheduleID, now)
			d.record(row.SessionID, "ScheduleTriggered", row.LastTaskID, "go", map[string]any{"schedule_id": row.ScheduleID, "status": "failed", "reason": "replace_cancel_failed", "error": err.Error()}, "")
			return true
		}
		d.record(row.SessionID, "ScheduleTriggered", row.LastTaskID, "go", map[string]any{"schedule_id": row.ScheduleID, "status": "replaced", "reason": "previous_task_cancelled"}, "")
		return false
	default:
		d.record(row.SessionID, "ScheduleTriggered", row.LastTaskID, "go", map[string]any{"schedule_id": row.ScheduleID, "status": "skipped", "reason": "overlap_forbidden"}, "")
		return true
	}
}

func mustScheduleParams(row *scheduler.Schedule) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"session_id": row.SessionID, "prompt": row.Prompt, "model": row.Model, "reasoning_effort": row.ReasoningEffort, "agent": row.Agent, "mode": row.Mode})
	return raw
}
