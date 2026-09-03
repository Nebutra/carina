package daemon

import (
	"encoding/json"
	"fmt"
	"sort"
)

// promptCacheReceipt is deliberately provider-honest. A stable prefix and a
// local hash prove what Carina offered to an adapter; only provider usage can
// prove a cache hit or write. Adapters without cache telemetry therefore show
// "unsupported" or "miss_or_unreported", never a guessed hit.
func promptCacheReceipt(cacheKind string, usage ModelUsage, stablePrefix string, boundaries ...string) map[string]any {
	boundary := "stable_prefix"
	if len(boundaries) > 0 && boundaries[0] != "" {
		boundary = boundaries[0]
	}
	status := "miss_or_unreported"
	evidence := "carina_stable_prefix_only"
	providerReported := usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0
	switch {
	case usage.CacheReadTokens > 0:
		status, evidence = "hit", "provider_usage"
	case usage.CacheWriteTokens > 0:
		status, evidence = "write", "provider_usage"
	case cacheKind == "none":
		status, evidence = "unsupported", "adapter_capability"
	}
	return map[string]any{
		"status":               status,
		"evidence":             evidence,
		"provider_reported":    providerReported,
		"provider":             usage.Provider,
		"model":                usage.Model,
		"cache_kind":           cacheKind,
		"cache_read_tokens":    usage.CacheReadTokens,
		"cache_write_tokens":   usage.CacheWriteTokens,
		"stable_prefix_sha256": sha256Hex(stablePrefix),
		"stable_prefix_bytes":  len(stablePrefix),
		"boundary":             boundary,
	}
}

// promptCacheReceiptForSegments hashes the bytes the adapter can actually
// cache. Anthropic marks only constitution A-D; workspace, project rules, and
// memory are deliberately uncached system blocks. OpenAI's cache key covers
// the complete frozen StablePrefix. Unsupported adapters still report the
// offered stable prefix without implying that the provider accepted it.
func promptCacheReceiptForSegments(cacheKind string, usage ModelUsage, seg promptSegments) map[string]any {
	prefix := seg.StablePrefix
	boundary := "stable_prefix"
	if cacheKind == "anthropic" {
		prefix = joinPromptPrefix(seg.ConstitutionSections()...)
		boundary = "system_sections_a_d"
	}
	return promptCacheReceipt(cacheKind, usage, prefix, boundary)
}

const (
	promptCacheSLOPercent = 95
	promptCacheMinSample  = 20
)

// promptCacheSLOFromEvents turns canonical cache receipts into a release-gate
// measurement. The first cache-eligible request is warm-up and is excluded
// from the hit-rate denominator. Unsupported adapters are counted but never
// diluted into the provider-cache SLO.
func promptCacheSLOFromEvents(raw []byte, taskID string) map[string]any {
	var events []struct {
		Type    string         `json:"type"`
		TaskID  string         `json:"task_id"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		return map[string]any{"available": false, "reason": "cache receipts could not be decoded: " + err.Error()}
	}
	counts := map[string]int{"hit": 0, "write": 0, "miss_or_unreported": 0, "unsupported": 0}
	kinds := map[string]bool{}
	attempts, eligible, providerReported := 0, 0, 0
	postWarmupEligible, postWarmupHits := 0, 0
	warmed := false
	for _, event := range events {
		if event.Type != "PromptCacheObserved" || event.TaskID != taskID {
			continue
		}
		attempts++
		status, _ := event.Payload["status"].(string)
		if _, ok := counts[status]; ok {
			counts[status]++
		}
		if kind, _ := event.Payload["cache_kind"].(string); kind != "" {
			kinds[kind] = true
		}
		if reported, _ := event.Payload["provider_reported"].(bool); reported {
			providerReported++
		}
		if status == "unsupported" {
			continue
		}
		if status != "hit" && status != "write" && status != "miss_or_unreported" {
			continue
		}
		eligible++
		if !warmed {
			warmed = true
			continue
		}
		postWarmupEligible++
		if status == "hit" {
			postWarmupHits++
		}
	}
	kindList := make([]string, 0, len(kinds))
	for kind := range kinds {
		kindList = append(kindList, kind)
	}
	sort.Strings(kindList)
	rate := 0.0
	if postWarmupEligible > 0 {
		rate = float64(postWarmupHits) * 100 / float64(postWarmupEligible)
	}
	sufficient := postWarmupEligible >= promptCacheMinSample
	return map[string]any{
		"available": attempts > 0, "attempts": attempts, "eligible_requests": eligible,
		"hits": counts["hit"], "writes": counts["write"],
		"miss_or_unreported": counts["miss_or_unreported"], "unsupported": counts["unsupported"],
		"provider_reported_requests": providerReported, "cache_kinds": kindList,
		"post_warmup_requests": postWarmupEligible, "post_warmup_hits": postWarmupHits,
		"post_warmup_hit_rate_percent": rate,
		"target_percent":               promptCacheSLOPercent, "minimum_sample": promptCacheMinSample,
		"sufficient_sample": sufficient,
		"meets_target":      sufficient && rate >= promptCacheSLOPercent,
	}
}

func (d *Daemon) promptCacheSLO(sessionID, taskID string) map[string]any {
	if d == nil || d.kern == nil {
		return map[string]any{"available": false, "reason": "audit service unavailable"}
	}
	raw, err := d.kern.ReadEvents(sessionID)
	if err != nil {
		return map[string]any{"available": false, "reason": fmt.Sprintf("cache receipts unavailable: %v", err)}
	}
	return promptCacheSLOFromEvents(raw, taskID)
}
