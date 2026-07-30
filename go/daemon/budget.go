package daemon

import (
	"encoding/json"
	"fmt"
)

func (d *Daemon) handleTaskBudgetExtend(params json.RawMessage) (any, error) {
	d.checkpointMu.Lock()
	defer d.checkpointMu.Unlock()
	var p struct {
		RunID            string `json:"run_id"`
		AdditionalTokens int    `json:"additional_tokens"`
		Approver         string `json:"approver"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.AdditionalTokens <= 0 {
		return nil, fmt.Errorf("additional_tokens must be positive")
	}
	task, ok := d.sched.Get(p.RunID)
	if !ok {
		return nil, fmt.Errorf("unknown execution %s", p.RunID)
	}
	if task.Status != "needs_input" {
		return nil, fmt.Errorf("execution %s is %s, not awaiting budget approval", p.RunID, task.Status)
	}
	sess, ok := d.store.Get(task.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", task.SessionID)
	}
	fence := d.sessionExecutionFence(task.SessionID)
	fence.RLock()
	defer fence.RUnlock()
	cp := d.runs.loadCheckpoint(task.RunID)
	if cp == nil {
		return nil, fmt.Errorf("task %s has no durable checkpoint and cannot be resumed safely", task.RunID)
	}
	d.sched.SetTokenBudget(task.RunID, task.TokenBudget+p.AdditionalTokens)
	d.sched.SetStatus(task.RunID, "running")
	updated, _ := d.sched.Get(task.RunID)
	d.record(task.SessionID, "ExecutionProgressed", task.RunID, "go", map[string]any{"status": "budget_extended", "additional_tokens": p.AdditionalTokens, "token_budget": updated.TokenBudget, "approver": p.Approver}, "")
	d.persistRun(task.RunID)
	d.startTask(func() { d.resumeTaskGuarded(sess, updated, cp) })
	return updated, nil
}
