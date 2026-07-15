package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
)

func (d *Daemon) handleContextSummary(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	var latest *scheduler.Task
	for _, task := range d.sched.List() {
		if task.SessionID != p.SessionID {
			continue
		}
		if latest == nil || task.UpdatedAt.After(latest.UpdatedAt) {
			latest = task
		}
	}
	out := map[string]any{
		"session_id": p.SessionID,
		"model_context_tokens": map[string]any{
			"available": false,
			"reason":    "providers report request usage, but Carina does not persist a checkpoint-linked tokenizer count or model context limit",
		},
		"compact": map[string]any{
			"available": false,
			"reason":    "no safe operator transaction exists to compact and atomically replace a running task checkpoint; context.compress is diagnostic content compression only",
		},
	}
	if latest == nil {
		out["checkpoint"] = map[string]any{"available": false, "reason": "session has no task checkpoint"}
		return out, nil
	}
	out["task"] = map[string]any{"task_id": latest.TaskID, "status": latest.Status, "tokens_used": latest.TokensUsed, "token_usage_observed": latest.TokenUsageObserved, "token_budget": latest.TokenBudget}
	cp := d.runs.loadCheckpoint(latest.TaskID)
	if cp == nil || cp.Transcript == nil {
		out["checkpoint"] = map[string]any{"available": false, "reason": "latest task has no persisted checkpoint"}
		return out, nil
	}
	out["checkpoint"] = map[string]any{
		"available": true, "checkpoint_id": checkpointID(latest, cp), "turn": cp.Turn,
		"transcript_bytes": cp.Transcript.size(), "turn_count": len(cp.Transcript.Turns),
		"summary_bytes": len(cp.Transcript.Summary), "compaction_count": len(cp.Transcript.CompactionReceipts),
		"memory_snapshot_bytes": len(cp.MemorySnapshot),
		"measurement":           "exact persisted checkpoint bytes; not token or live in-flight context usage",
	}
	return out, nil
}
