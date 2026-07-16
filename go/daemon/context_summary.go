package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/provider"
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
			"reason":    "compact requires a persisted checkpoint at a paused task boundary",
		},
	}
	if latest == nil {
		out["checkpoint"] = map[string]any{"available": false, "reason": "session has no task checkpoint"}
		return out, nil
	}
	out["task"] = map[string]any{"task_id": latest.TaskID, "status": latest.Status, "tokens_used": latest.TokensUsed, "token_usage_observed": latest.TokenUsageObserved, "token_budget": latest.TokenBudget}
	if usage, ok := d.usage.latestTaskContext(latest.TaskID); ok {
		used := usage.InputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
		context := map[string]any{
			"available": !usage.Estimated, "tokens": used, "measurement": "latest completed provider request",
			"provider": usage.Provider, "model": usage.Model, "estimated": usage.Estimated,
			"breakdown": map[string]any{"input_tokens": usage.InputTokens, "cache_read_tokens": usage.CacheReadTokens, "cache_write_tokens": usage.CacheWriteTokens},
		}
		if usage.Estimated {
			context["reason"] = "the active reasoner did not return provider token usage; tokens are explicitly estimated"
		}
		if limit, ok := modelContextLimit(d.providerCatalog, usage.Provider, usage.Model); ok {
			remaining := max(0, limit-used)
			percent := 0
			if limit > 0 {
				percent = minInt(100, used*100/limit)
			}
			level := "normal"
			if percent >= 90 {
				level = "critical"
			} else if percent >= 80 {
				level = "warning"
			}
			context["limit_tokens"], context["remaining_tokens"], context["used_percent"], context["threshold"] = limit, remaining, percent, level
		}
		out["model_context_tokens"] = context
	}
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
	if latest.Status == "paused" && !latest.ReconciliationRequired {
		out["compact"] = map[string]any{"available": true, "method": "session.checkpoint.compact", "checkpoint_id": checkpointID(latest, cp), "safety": "WAL-backed immutable child checkpoint; source preserved"}
	}
	return out, nil
}

func modelContextLimit(catalog provider.Catalog, providerID, modelID string) (int, bool) {
	info, ok := catalog[normalizeProviderID(providerID)]
	if !ok {
		return 0, false
	}
	modelID = strings.TrimPrefix(strings.TrimSpace(modelID), normalizeProviderID(providerID)+"/")
	if model, ok := info.Models[modelID]; ok && model.Limit.Context > 0 {
		return model.Limit.Context, true
	}
	for key, model := range info.Models {
		if (model.ID == modelID || key == modelID) && model.Limit.Context > 0 {
			return model.Limit.Context, true
		}
	}
	return 0, false
}
