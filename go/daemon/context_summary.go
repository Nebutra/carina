package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
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
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	var latest *scheduler.ExecutionRun
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
			"reason":    "compact requires an idle task with a persisted checkpoint (not mid-execution)",
		},
	}
	if latest == nil {
		out["checkpoint"] = map[string]any{"available": false, "reason": "session has no task checkpoint"}
		out["ledger"] = map[string]any{
			"available": false,
			"reason":    "session has no task; there is no model-visible prompt to project",
		}
		return out, nil
	}
	out["task"] = map[string]any{"task_id": latest.RunID, "status": latest.Status, "mode": latest.Mode, "tokens_used": latest.TokensUsed, "token_usage_observed": latest.TokenUsageObserved, "token_budget": latest.TokenBudget}
	if usage, ok := d.usage.latestTaskContext(latest.RunID); ok {
		used := usage.InputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
		context := map[string]any{
			"available": !usage.Estimated, "tokens": used, "measurement": "latest completed provider request",
			"provider": usage.Provider, "model": usage.Model, "estimated": usage.Estimated,
			"breakdown": map[string]any{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "cache_read_tokens": usage.CacheReadTokens, "cache_write_tokens": usage.CacheWriteTokens},
		}
		if usage.Estimated {
			context["reason"] = "the active reasoner did not return provider token usage; tokens are explicitly estimated"
		}
		modelRef := usage.Model
		if p := strings.TrimSpace(usage.Provider); p != "" {
			if !strings.Contains(modelRef, "/") {
				modelRef = p + "/" + strings.TrimSpace(usage.Model)
			}
		}
		if limit, source, ok := resolveModelContextLimit(d.providerCatalog, modelRef); ok {
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
			context["metadata_source"] = source
			// Real catalog windows (including alias) are not "guessed" 32k.
			context["estimated_limit"] = false
		}
		out["model_context_tokens"] = context
	}
	cp := d.runs.loadCheckpoint(latest.RunID)
	if cp == nil || cp.Transcript == nil {
		out["checkpoint"] = map[string]any{"available": false, "reason": "latest task has no persisted checkpoint"}
		out["ledger"] = d.contextLedger(sess, latest, nil, "")
		return out, nil
	}
	policy := cp.Transcript.CompactionBudget
	out["compaction_policy"] = map[string]any{
		"policy_version": policy.PolicyVersion, "window_tokens": policy.WindowTokens,
		"reserve_tokens": policy.ReserveTokens, "trigger_tokens": policy.TriggerTokens,
		"metadata_source": policy.MetadataSource,
	}
	if context, ok := out["model_context_tokens"].(map[string]any); ok && policy.WindowTokens > 0 {
		if _, hasLimit := context["limit_tokens"]; !hasLimit {
			used, _ := context["tokens"].(int)
			context["limit_tokens"] = policy.WindowTokens
			context["remaining_tokens"] = max(0, policy.WindowTokens-used)
			context["used_percent"] = minInt(100, used*100/policy.WindowTokens)
			context["metadata_source"] = policy.MetadataSource
			context["estimated_limit"] = policy.MetadataSource != "catalog" && policy.MetadataSource != "catalog-alias"
		} else if _, hasSource := context["metadata_source"]; !hasSource {
			context["metadata_source"] = policy.MetadataSource
			if _, hasEst := context["estimated_limit"]; !hasEst {
				context["estimated_limit"] = policy.MetadataSource != "catalog" && policy.MetadataSource != "catalog-alias"
			}
		}
	}
	out["checkpoint"] = map[string]any{
		"available": true, "checkpoint_id": checkpointID(latest, cp), "turn": cp.Turn,
		"transcript_bytes": cp.Transcript.size(), "turn_count": len(cp.Transcript.Turns),
		"summary_bytes": len(cp.Transcript.Summary), "compaction_count": len(cp.Transcript.CompactionReceipts),
		"memory_snapshot_bytes": len(cp.MemorySnapshot),
		"measurement":           "exact persisted checkpoint bytes; not token or live in-flight context usage",
	}
	if receipts := cp.Transcript.CompactionReceipts; len(receipts) > 0 {
		out["recent_receipt"] = receipts[len(receipts)-1]
	}
	out["ledger"] = d.contextLedger(sess, latest, cp.Transcript, cp.MemorySnapshot)
	// Compact is available at any idle turn boundary with a checkpoint — not
	// only paused. Live mid-execution stays refused (activeSessionTask / fence).
	if latest.ReconciliationRequired {
		out["compact"] = map[string]any{"available": false, "reason": "checkpoint reconciliation required before compact"}
	} else if active := d.activeSessionTask(p.SessionID); active != nil {
		out["compact"] = map[string]any{
			"available": false,
			"reason":    fmt.Sprintf("session task %s is %s; compact waits for an idle turn boundary", active.id, active.status),
		}
	} else if compactStatusOK(latest.Status) {
		out["compact"] = map[string]any{
			"available": true, "method": "session.checkpoint.compact",
			"checkpoint_id": checkpointID(latest, cp),
			"safety":        "WAL-backed immutable child checkpoint; source preserved; idle-task boundary (not mid-execution)",
			"task_status":   latest.Status,
		}
	} else {
		out["compact"] = map[string]any{
			"available": false,
			"reason":    fmt.Sprintf("task status %s is not idle enough for compact", latest.Status),
		}
	}
	return out, nil
}

func (d *Daemon) contextLedger(sess *sessionstore.Session, task *scheduler.ExecutionRun, tr *Transcript, memorySnapshot string) map[string]any {
	model := taskModel(task)
	cache := d.promptCacheKind(model)
	nativeEligible := d.nativeToolsEligible(d.reasoner, model)
	instruction := "Respond with the next action as a single JSON object."
	var layers promptLayers
	if sess != nil && task != nil {
		layers = d.composeAgentPromptLayers(sess, task, memorySnapshot)
		if nativeEligible {
			layers = layers.withToolContract(nativeToolsContract)
			instruction = "Call the next tool. Use done when the task is finished."
		}
	}
	visible := ""
	if tr != nil {
		visible = tr.render()
	}
	userPrompt := ""
	if task != nil {
		userPrompt = task.UserPrompt
	}
	seg := buildPromptSegmentsFromLayers(layers, userPrompt, visible, instruction)
	layer := func(id, text, layerCache string) map[string]any {
		return map[string]any{
			"id":               id,
			"bytes":            len(text),
			"tokens_estimated": estimateTokens(text),
			"estimated":        true,
			"estimate_method":  "chars/4",
			"cache":            layerCache,
		}
	}
	ledger := map[string]any{
		"available":                      tr != nil,
		"cache":                          cache,
		"estimate_method":                "chars/4",
		"estimated":                      true,
		"model_visible":                  visible,
		"model_visible_bytes":            len(visible),
		"model_visible_sha256":           sha256Hex(visible),
		"model_visible_tokens_estimated": estimateTokens(visible),
		"layers": []map[string]any{
			layer("constitution", layers.Constitution, cache),
			layer("workspace", layers.Workspace, cache),
			layer("catalog", layers.Catalog, cache),
			layer("trailer", seg.taskTrailer, "none"),
			layer("transcript", visible, "none"),
		},
		"elided_turns": transcriptTurnIndices(tr, func(turn Turn) bool { return turn.Obs.Elided }),
		"pinned_turns": transcriptTurnIndices(tr, func(turn Turn) bool { return turn.Obs.Pinned }),
		"receipts":     []CompactionReceipt{},
	}
	if tr == nil {
		ledger["reason"] = "latest task has no persisted checkpoint; layers are the next-turn prefix only"
	}
	if tr != nil && len(tr.CompactionReceipts) > 0 {
		ledger["receipts"] = tr.CompactionReceipts
	}
	return ledger
}

func (d *Daemon) promptCacheKind(model string) string {
	providerID, _, _ := strings.Cut(strings.TrimSpace(model), "/")
	if strings.EqualFold(providerID, provider.GrokBuildProviderID) {
		return "none"
	}
	if d != nil {
		if rr, ok := d.reasoner.(*routerReasoner); ok {
			if _, _, routed := rr.claudeCodeRoute(model); routed {
				return "none"
			}
		}
		if d.reasoner != nil {
			return "anthropic"
		}
	}
	return "none"
}

func transcriptTurnIndices(tr *Transcript, keep func(Turn) bool) []int {
	if tr == nil {
		return []int{}
	}
	out := make([]int, 0)
	for _, turn := range tr.Turns {
		if keep(turn) {
			out = append(out, turn.Index)
		}
	}
	return out
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
