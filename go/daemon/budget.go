package daemon

import (
	"encoding/json"
	"fmt"
)

func (d *Daemon) handleTaskBudgetExtend(params json.RawMessage) (any, error) {
	d.checkpointMu.Lock()
	defer d.checkpointMu.Unlock()
	var p struct {
		TaskID           string `json:"task_id"`
		AdditionalTokens int    `json:"additional_tokens"`
		Approver         string `json:"approver"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.AdditionalTokens <= 0 {
		return nil, fmt.Errorf("additional_tokens must be positive")
	}
	task, ok := d.sched.Get(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("unknown task %s", p.TaskID)
	}
	if task.Status != "needs_input" {
		return nil, fmt.Errorf("task %s is %s, not awaiting budget approval", p.TaskID, task.Status)
	}
	sess, ok := d.store.Get(task.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", task.SessionID)
	}
	fence := d.sessionExecutionFence(task.SessionID)
	fence.RLock()
	defer fence.RUnlock()
	cp := d.runs.loadCheckpoint(task.TaskID)
	if cp == nil {
		return nil, fmt.Errorf("task %s has no durable checkpoint and cannot be resumed safely", task.TaskID)
	}
	d.sched.SetTokenBudget(task.TaskID, task.TokenBudget+p.AdditionalTokens)
	d.sched.SetStatus(task.TaskID, "running")
	updated, _ := d.sched.Get(task.TaskID)
	d.record(task.SessionID, "TaskCreated", task.TaskID, "go", map[string]any{"status": "budget_extended", "additional_tokens": p.AdditionalTokens, "token_budget": updated.TokenBudget, "approver": p.Approver}, "")
	d.persistRun(task.TaskID)
	d.startTask(func() { d.resumeTaskGuarded(sess, updated, cp) })
	return updated, nil
}
