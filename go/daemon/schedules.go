package daemon

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
)

func (d *Daemon) handleScheduleCreate(params json.RawMessage) (any, error) {
	var p struct {
		SessionID  string `json:"session_id"`
		Prompt     string `json:"prompt"`
		Kind       string `json:"kind"`
		Expression string `json:"expression"`
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
	row, err := d.schedules.Create(p.SessionID, p.Prompt, p.Kind, p.Expression, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	d.record(p.SessionID, "ScheduleChanged", "", "go", map[string]any{"action": "created", "schedule": row}, "")
	return row, nil
}

func (d *Daemon) handleScheduleList(json.RawMessage) (any, error) {
	return map[string]any{"schedules": d.schedules.List()}, nil
}

func (d *Daemon) handleSchedulePause(params json.RawMessage) (any, error) {
	id, err := scheduleID(params)
	if err != nil {
		return nil, err
	}
	row, err := d.schedules.SetEnabled(id, false, time.Now().UTC())
	if err == nil {
		d.record(row.SessionID, "ScheduleChanged", "", "go", map[string]any{"action": "paused", "schedule_id": id}, "")
	}
	return row, err
}

func (d *Daemon) handleScheduleResume(params json.RawMessage) (any, error) {
	id, err := scheduleID(params)
	if err != nil {
		return nil, err
	}
	row, err := d.schedules.SetEnabled(id, true, time.Now().UTC())
	if err == nil {
		d.record(row.SessionID, "ScheduleChanged", "", "go", map[string]any{"action": "resumed", "schedule_id": id}, "")
	}
	return row, err
}

func (d *Daemon) handleScheduleDelete(params json.RawMessage) (any, error) {
	id, err := scheduleID(params)
	if err != nil {
		return nil, err
	}
	row, err := d.schedules.Delete(id)
	if err != nil {
		return nil, err
	}
	d.record(row.SessionID, "ScheduleChanged", "", "go", map[string]any{"action": "deleted", "schedule_id": id}, "")
	return map[string]any{"deleted": true, "schedule_id": id}, nil
}

func scheduleID(params json.RawMessage) (string, error) {
	var p struct {
		ScheduleID string `json:"schedule_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.ScheduleID == "" {
		return "", fmt.Errorf("schedule_id is required")
	}
	return p.ScheduleID, nil
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
				result, err := d.handleTaskSubmit(mustScheduleParams(row.SessionID, row.Prompt))
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

func mustScheduleParams(sessionID, prompt string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"session_id": sessionID, "prompt": prompt})
	return raw
}
