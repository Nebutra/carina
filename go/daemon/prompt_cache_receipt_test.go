package daemon

import (
	"encoding/json"
	"testing"
)

func TestPromptCacheReceiptRequiresProviderEvidence(t *testing.T) {
	miss := promptCacheReceipt("anthropic", ModelUsage{Provider: "anthropic", Model: "claude"}, "stable")
	if miss["status"] != "miss_or_unreported" || miss["provider_reported"] != false || miss["evidence"] != "carina_stable_prefix_only" {
		t.Fatalf("unreported cache must stay honest: %#v", miss)
	}
	hit := promptCacheReceipt("anthropic", ModelUsage{Provider: "anthropic", Model: "claude", CacheReadTokens: 12}, "stable")
	if hit["status"] != "hit" || hit["evidence"] != "provider_usage" || hit["cache_read_tokens"] != 12 {
		t.Fatalf("provider cache hit receipt = %#v", hit)
	}
	unsupported := promptCacheReceipt("none", ModelUsage{Provider: "grok-build", Model: "grok"}, "stable")
	if unsupported["status"] != "unsupported" || unsupported["evidence"] != "adapter_capability" {
		t.Fatalf("unsupported adapter receipt = %#v", unsupported)
	}
}

func TestPromptCacheSLOExcludesWarmupAndRequiresSample(t *testing.T) {
	type event struct {
		Type    string         `json:"type"`
		TaskID  string         `json:"task_id"`
		Payload map[string]any `json:"payload"`
	}
	var events []event
	add := func(status string, reported bool) {
		events = append(events, event{Type: "PromptCacheObserved", TaskID: "task", Payload: map[string]any{
			"status": status, "provider_reported": reported, "cache_kind": "anthropic",
		}})
	}
	add("write", true) // warm-up; excluded from the post-warm-up denominator
	for range 20 {
		add("hit", true)
	}
	add("miss_or_unreported", false)
	events = append(events, event{Type: "PromptCacheObserved", TaskID: "other", Payload: map[string]any{"status": "hit"}})
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	slo := promptCacheSLOFromEvents(raw, "task")
	if slo["attempts"] != 22 || slo["post_warmup_requests"] != 21 || slo["post_warmup_hits"] != 20 {
		t.Fatalf("cache SLO counts = %#v", slo)
	}
	if slo["sufficient_sample"] != true || slo["meets_target"] != true {
		t.Fatalf("20/21 post-warm-up hits should satisfy the 95%% gate: %#v", slo)
	}

	shortRaw, _ := json.Marshal(events[:2])
	short := promptCacheSLOFromEvents(shortRaw, "task")
	if short["sufficient_sample"] != false || short["meets_target"] != false {
		t.Fatalf("short sample must not pass the release gate: %#v", short)
	}
}

func TestPromptCacheReceiptUsesAdapterActualBoundary(t *testing.T) {
	layers := promptLayers{
		Mode: "MODE", Identity: "IDENTITY", Protocol: "PROTOCOL", Tools: "TOOLS",
		Workspace: "WORKSPACE", Catalog: "CATALOG",
	}
	layers.StablePrefix = layers.assembledStablePrefix()
	seg := buildPromptSegmentsFromLayers(layers, "task", "turn", "done")

	anthropic := promptCacheReceiptForSegments("anthropic", ModelUsage{Provider: "anthropic", Model: "claude"}, seg)
	cacheable := joinPromptPrefix(seg.ConstitutionSections()...)
	if anthropic["boundary"] != "system_sections_a_d" || anthropic["stable_prefix_sha256"] != sha256Hex(cacheable) || anthropic["stable_prefix_bytes"] != len(cacheable) {
		t.Fatalf("Anthropic receipt does not match cached A-D bytes: %#v", anthropic)
	}
	if anthropic["stable_prefix_sha256"] == sha256Hex(seg.StablePrefix) {
		t.Fatal("Anthropic receipt incorrectly included uncached workspace/catalog blocks")
	}

	openAI := promptCacheReceiptForSegments("openai_key", ModelUsage{Provider: "openai", Model: "gpt"}, seg)
	if openAI["boundary"] != "stable_prefix" || openAI["stable_prefix_sha256"] != sha256Hex(seg.StablePrefix) {
		t.Fatalf("OpenAI receipt must cover its prompt_cache_key prefix: %#v", openAI)
	}
}
