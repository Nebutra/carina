package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const nativeSchemaMaxWindowPercent = 15

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
	sess, err := d.requireNamedSession(p.SessionID, params)
	if err != nil {
		return nil, err
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
		"metadata_source": policy.MetadataSource, "lookahead_tokens": policy.LookaheadTokens,
		"semantic_enabled": policy.SemanticEnabled,
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
		"memory_snapshot_bytes":       len(cp.MemorySnapshot),
		"instruction_manifest_digest": instructionManifestDigest(cp.InstructionManifest),
		"measurement":                 "exact persisted checkpoint bytes; not token or live in-flight context usage",
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
	latestUsage := ModelUsage{Model: model}
	hasUsage := false
	if d != nil && d.usage != nil && task != nil {
		if usage, ok := d.usage.latestTaskContext(task.RunID); ok {
			latestUsage = usage
			hasUsage = true
			if ref := effectiveModelName(usage); ref != "" {
				model = ref
			}
		}
	}
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
	var nativeSpecs []modelrouter.ToolSpec
	if nativeEligible {
		nativeSpecs = d.builtinNativeToolSpecsFor(sess)
	}
	nativeSchemaTokens := 0
	for _, spec := range nativeSpecs {
		raw, _ := json.Marshal(spec)
		nativeSchemaTokens += estimateTokens(string(raw))
	}
	contextBudget := resolveCompactionBudget(d.providerCatalog, model)
	nativeSchemaShare := 0.0
	if contextBudget.WindowTokens > 0 {
		nativeSchemaShare = float64(nativeSchemaTokens) * 100 / float64(contextBudget.WindowTokens)
	}
	nativeSchemaStatus := "within_budget"
	if nativeSchemaShare >= nativeSchemaMaxWindowPercent {
		nativeSchemaStatus = "exceeded"
	}
	viewTokens := estimateTokens(visible)
	requestTokens := 0
	requestEstimated := true
	requestMethod := "unavailable"
	if hasUsage {
		requestTokens = latestUsage.promptTokens()
		requestEstimated = latestUsage.Estimated
		requestMethod = "provider_usage"
		if latestUsage.Estimated {
			requestMethod = "reasoner_estimate"
		}
	}
	layer := func(id, text, layerCache, role string) map[string]any {
		return map[string]any{
			"id":               id,
			"bytes":            len(text),
			"tokens_estimated": estimateTokens(text),
			"estimated":        true,
			"estimate_method":  "chars/4",
			"cache":            layerCache,
			"role":             role,
		}
	}
	constitutionRole := "user"
	dynamicCache := cache
	if cache == "anthropic" {
		constitutionRole = "system"
		dynamicCache = "dynamic"
	}
	ledger := map[string]any{
		"available":                      tr != nil,
		"cache":                          cache,
		"constitution_role":              constitutionRole,
		"estimate_method":                "chars/4",
		"estimated":                      true,
		"model_visible":                  visible,
		"model_visible_bytes":            len(visible),
		"model_visible_sha256":           sha256Hex(visible),
		"model_visible_tokens_estimated": viewTokens,
		"stable_prefix_bytes":            len(seg.StablePrefix),
		"stable_prefix_sha256":           sha256Hex(seg.StablePrefix),
		"cache_boundary":                 "stable_prefix",
		"cache_receipt":                  promptCacheReceiptForSegments(cache, latestUsage, seg),
		"native_tool_count":              len(nativeSpecs),
		"native_schema_tokens_estimated": nativeSchemaTokens,
		"native_schema_share_percent":    nativeSchemaShare,
		"native_schema_budget_percent":   nativeSchemaMaxWindowPercent,
		"native_schema_budget_status":    nativeSchemaStatus,
		"native_schema_window_tokens":    contextBudget.WindowTokens,
		"native_schema_window_source":    contextBudget.Source,
		"layers": append(append(compactPromptLedgerLayers(layer, cache, constitutionRole, []struct{ id, text string }{
			{"mode", layers.Mode},
			{"identity", layers.Identity},
			{"protocol", layers.Protocol},
			{"tools", layers.Tools},
		}), compactPromptLedgerLayers(layer, dynamicCache, constitutionRole, []struct{ id, text string }{
			{"workspace", layers.Workspace},
			{"catalog", layers.Catalog},
		})...), layer("trailer", seg.taskTrailer, "none", "user"), layer("transcript", visible, "none", "user")),
		"elided_turns":        transcriptTurnIndices(tr, func(turn Turn) bool { return turn.Obs.Elided }),
		"pinned_turns":        transcriptTurnIndices(tr, func(turn Turn) bool { return turn.Obs.Pinned }),
		"receipts":            []CompactionReceipt{},
		"summarizer_failures": 0,
		"summarizer_circuit":  "closed",
	}
	if hasUsage {
		ledger["latest_request_input_tokens"] = requestTokens
		ledger["latest_request_input_estimated"] = requestEstimated
		ledger["latest_request_input_method"] = requestMethod
	}
	if sess != nil && task != nil {
		ledger["cache_slo"] = d.promptCacheSLO(sess.SessionID, task.RunID)
	} else {
		ledger["cache_slo"] = map[string]any{"available": false, "reason": "session or task unavailable"}
	}
	if !hasUsage {
		ledger["cache_receipt"] = promptCacheReceiptForSegments(cache, ModelUsage{Provider: "", Model: model}, seg)
	}
	if tr != nil {
		ledger["summarizer_failures"] = tr.SummarizerFailures
		ledger["durable_fact_count"] = len(tr.DurableFacts)
		ledger["durable_facts"] = append([]CompactionFact(nil), tr.DurableFacts...)
		if tr.summarizerCircuitOpen() {
			ledger["summarizer_circuit"] = "open"
		}
	}
	if tr == nil {
		ledger["reason"] = "latest task has no persisted checkpoint; layers are the next-turn prefix only"
	}
	if tr != nil && len(tr.CompactionReceipts) > 0 {
		ledger["receipts"] = tr.CompactionReceipts
	}
	return ledger
}

func compactPromptLedgerLayers(layer func(id, text, cache, role string) map[string]any, cache, role string, items []struct{ id, text string }) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.text) == "" {
			continue
		}
		out = append(out, layer(item.id, item.text, cache, role))
	}
	return out
}

func (d *Daemon) promptCacheKind(model string) string {
	var catalog provider.Catalog
	var reasoner Reasoner
	if d != nil {
		catalog = d.providerCatalog
		reasoner = d.reasoner
	}
	return promptCacheKindFor(catalog, reasoner, model)
}

// promptCacheKindFor labels prefix-cache capability. Anthropic Messages uses
// explicit cache_control breakpoints. The first-party OpenAI HTTP adapter uses
// a stable prompt_cache_key; compatible relays and CLI routes remain none.
func promptCacheKindFor(catalog provider.Catalog, reasoner Reasoner, model string) string {
	model = strings.TrimSpace(model)
	if model == "" || strings.EqualFold(model, "default") {
		return "none"
	}
	providerID, _, _ := strings.Cut(model, "/")
	if strings.EqualFold(providerID, provider.GrokBuildProviderID) {
		return "none"
	}
	if rr, ok := reasoner.(*routerReasoner); ok {
		if _, _, routed := rr.claudeCodeRoute(model); routed {
			return "none"
		}
	}
	if info, ok := catalog[normalizeProviderID(providerID)]; ok {
		protocol := detectRuntimeProtocol(info)
		if protocol == protocolAnthropic {
			return "anthropic"
		}
		if _, routedByHTTP := reasoner.(*routerReasoner); routedByHTTP && strings.EqualFold(providerID, "openai") &&
			(protocol == protocolOpenAIChat || protocol == protocolOpenAIResponses) {
			return "openai_key"
		}
		return "none"
	}
	if strings.EqualFold(providerID, "anthropic") {
		return "anthropic"
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
