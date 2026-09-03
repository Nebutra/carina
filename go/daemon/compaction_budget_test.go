package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/provider"
)

func TestResolveCompactionBudgetUsesCatalogWindow(t *testing.T) {
	catalog := provider.Catalog{"anthropic": {
		ID: "anthropic",
		Models: map[string]provider.Model{
			"claude": {ID: "claude", Limit: provider.ModelLimit{Context: 200_000}},
		},
	}}
	budget := resolveCompactionBudget(catalog, "anthropic/claude")
	if budget.WindowTokens != 200_000 || budget.ReserveTokens != 20_000 || budget.TriggerTokens != 144_000 || budget.Source != "catalog" {
		t.Fatalf("budget = %+v", budget)
	}
	tr := newTranscript("task")
	applyCompactionBudget(tr, catalog, "anthropic/claude")
	if tr.policy.MaxChars != 576_000 || tr.policy.MaxTokens != 144_000 {
		t.Fatalf("policy = %+v", tr.policy)
	}
	if tr.policy.LookaheadTokens == 0 || !tr.policy.SemanticCompaction {
		t.Fatalf("production policy must enable proactive/semantic triggers: %+v", tr.policy)
	}
	tr.addTurn(Turn{Tool: "read", Obs: Observation{Content: strings.Repeat("x", 24_001), Pinned: true}})
	if tr.size() <= 24_000 {
		t.Fatalf("fixture did not exceed legacy threshold: %d", tr.size())
	}
	if tr.shouldCompact() {
		t.Fatal("200k model must not compact at the legacy 24k-char threshold")
	}
}

func TestCompactionBudgetSurvivesCheckpointDecode(t *testing.T) {
	tr := newTranscript("task")
	applyCompactionBudget(tr, provider.Catalog{"p": {ID: "p", Models: map[string]provider.Model{"m": {ID: "m", Limit: provider.ModelLimit{Context: 128_000}}}}}, "p/m")
	raw, err := json.Marshal(runCheckpoint{Turn: 2, Transcript: tr})
	if err != nil {
		t.Fatal(err)
	}
	cp := decodeRunCheckpoint(raw)
	if cp == nil || cp.Transcript.policy.MaxTokens != tr.policy.MaxTokens || cp.Transcript.policy.MetadataSource != "catalog" {
		t.Fatalf("decoded checkpoint lost model budget: %+v", cp)
	}
	if cp.Transcript.policy.LookaheadTokens != tr.policy.LookaheadTokens || !cp.Transcript.policy.SemanticCompaction {
		t.Fatalf("decoded checkpoint lost proactive policy: %+v", cp.Transcript.policy)
	}
}

func TestCompactionSupportsLookaheadAndSemanticTriggers(t *testing.T) {
	tr := newTranscript("task")
	tr.policy = CompactionPolicy{MaxChars: 1 << 20, MaxTokens: 100, LookaheadTokens: 10, KeepRecent: 1, SummarizeAfter: 1, ToolOutputMax: 1000, SemanticCompaction: true}
	tr.noteObservedInputTokens(92)
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Obs: Observation{Content: "evidence"}})
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read b.go", Obs: Observation{Content: "more evidence"}})
	if !tr.shouldCompact() || tr.compactionTrigger() != "proactive_lookahead" {
		t.Fatalf("lookahead trigger not active: should=%v trigger=%q", tr.shouldCompact(), tr.compactionTrigger())
	}
	tr.semanticShiftPending = false
	tr.noteObservedInputTokens(1)
	tr.addTurn(Turn{Tool: "user", ActionBrief: "steer", Obs: Observation{Content: "focus on the API"}})
	if !tr.shouldCompact() || tr.compactionTrigger() != "semantic_shift" {
		t.Fatalf("semantic trigger not active: should=%v trigger=%q", tr.shouldCompact(), tr.compactionTrigger())
	}
}

func TestResolveCompactionBudgetLabelsInvalidMetadataFallback(t *testing.T) {
	for name, window := range map[string]int{"missing": 0, "zero": 0, "tiny": 1_000, "absurd": 3_000_000} {
		t.Run(name, func(t *testing.T) {
			catalog := provider.Catalog{}
			if name != "missing" {
				catalog["p"] = provider.Info{ID: "p", Models: map[string]provider.Model{"m": {ID: "m", Limit: provider.ModelLimit{Context: window}}}}
			}
			budget := resolveCompactionBudget(catalog, "p/m")
			if budget.WindowTokens != fallbackContextTokens || budget.Source != "fallback-estimate" {
				t.Fatalf("budget = %+v", budget)
			}
		})
	}
}

func TestResolveCompactionBudgetAliasesManagedProxyToCatalogModel(t *testing.T) {
	catalog := provider.Catalog{
		"openai": {
			ID: "openai",
			Models: map[string]provider.Model{
				"gpt-5.6-sol": {ID: "gpt-5.6-sol", Limit: provider.ModelLimit{Context: 1_050_000}},
			},
		},
	}
	// Exact provider miss (CC Switch managed proxy), short model hits openai catalog.
	budget := resolveCompactionBudget(catalog, "ccswitch-codex-managed-proxy/gpt-5.6-sol")
	if budget.WindowTokens != 1_050_000 || budget.Source != "catalog-alias" {
		t.Fatalf("managed proxy budget = %+v", budget)
	}
	// Direct vendor route still stamps catalog (not alias).
	direct := resolveCompactionBudget(catalog, "openai/gpt-5.6-sol")
	if direct.WindowTokens != 1_050_000 || direct.Source != "catalog" {
		t.Fatalf("direct budget = %+v", direct)
	}
	// Unknown model still falls back to 32k estimate.
	missing := resolveCompactionBudget(catalog, "ccswitch-codex-managed-proxy/not-a-real-model")
	if missing.WindowTokens != fallbackContextTokens || missing.Source != "fallback-estimate" {
		t.Fatalf("missing model budget = %+v", missing)
	}
}

func TestCompactionReceiptStampsModelBudget(t *testing.T) {
	tr := newTranscript("task")
	tr.policy = CompactionPolicy{
		MaxChars: 1, MaxTokens: 1, KeepRecent: 1, ToolOutputMax: 1_000,
		SummarizeAfter: 1, VerbatimUserMaxChars: 1_000,
		PolicyVersion: compactionPolicyVersion, WindowTokens: 32_000,
		ReserveTokens: 4_000, MetadataSource: "fallback-estimate",
	}
	for i := 0; i < 3; i++ {
		tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Obs: Observation{Content: strings.Repeat("x", 20)}})
	}
	receipt := tr.compact(func(string) (string, error) { return "summary", nil })
	if receipt == nil || receipt.PolicyVersion != compactionPolicyVersion || receipt.MetadataSource != "fallback-estimate" || receipt.WindowTokens != 32_000 || receipt.PressureBefore <= 0 {
		t.Fatalf("receipt = %+v", receipt)
	}
}
