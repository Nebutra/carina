package daemon

import (
	"context"
	"testing"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

func TestLegacyReasonerGetsExplicitEstimatedUsage(t *testing.T) {
	r := &scriptedReasoner{steps: []string{`{"tool":"done","summary":"ok"}`}}
	result, err := thinkWithRetryModelResult(context.Background(), r, "legacy-model", "four token prompt")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Usage.Estimated || result.Usage.Provider != "scripted" || result.Usage.Model != "legacy-model" {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Usage.InputTokens == 0 || result.Usage.OutputTokens == 0 {
		t.Fatalf("estimated tokens must be populated: %+v", result.Usage)
	}
}

type segmentedProvider struct {
	req modelrouter.Request
}

func (p *segmentedProvider) Name() string { return "anthropic" }
func (p *segmentedProvider) Complete(_ context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	p.req = req
	return &modelrouter.Response{
		Provider: "anthropic", Model: "claude", Text: `{"tool":"done","summary":"ok"}`,
		InputTokens: 2, OutputTokens: 3, CacheReadTokens: 5, CacheWriteTokens: 7,
	}, nil
}

func TestRouterReasonerReturnsExactUsageAndPreservesSegments(t *testing.T) {
	router := modelrouter.New()
	p := &segmentedProvider{}
	router.RegisterProvider(p)
	r := newRouterReasoner(router, "anthropic/claude")

	result, err := thinkWithRetryModelSegments(context.Background(), r, "anthropic/claude", promptSegments{
		StablePrefix: "stable", VolatileSuffix: "volatile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.req.StablePrefix != "stable" || p.req.VolatileSuffix != "volatile" || p.req.Prompt != "stablevolatile" {
		t.Fatalf("request = %+v", p.req)
	}
	if result.Usage.Estimated || result.Usage.InputTokens != 2 || result.Usage.CacheReadTokens != 5 || result.Usage.CacheWriteTokens != 7 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestUsageStoreAggregatesFiltersCostsAndPersists(t *testing.T) {
	dir := t.TempDir()
	s := newUsageStore(dir)
	if err := s.record("sess-a", "task-a", ModelUsage{Provider: "anthropic", Model: "claude", InputTokens: 1_000_000, OutputTokens: 2_000_000, CacheReadTokens: 3_000_000, CacheWriteTokens: 4_000_000}); err != nil {
		t.Fatal(err)
	}
	if err := s.record("sess-a", "task-a", ModelUsage{Provider: "anthropic", Model: "claude", InputTokens: 10, Estimated: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.record("sess-b", "task-b", ModelUsage{Provider: "unknown", Model: "m", InputTokens: 99}); err != nil {
		t.Fatal(err)
	}

	catalog := provider.Catalog{"anthropic": {
		ID: "anthropic", Models: map[string]provider.Model{"claude": {
			ID: "claude", Cost: &provider.ModelCost{Input: 2, Output: 4, CacheRead: 0.5, CacheWrite: 2.5},
		}},
	}}
	got := s.costs("sess-a", "", catalog)
	if len(got.Providers) != 1 || got.Totals.Requests != 2 || !got.Estimated || !got.Totals.PricingKnown {
		t.Fatalf("cost response = %+v", got)
	}
	row := got.Providers[0]
	if row.InputTokens != 1_000_010 || row.CostUSD != 21.50002 {
		t.Fatalf("row = %+v", row)
	}

	reloaded := newUsageStore(dir)
	all := reloaded.costs("", "", catalog)
	if len(all.Providers) != 2 || all.Totals.PricingKnown {
		t.Fatalf("persisted/unknown pricing response = %+v", all)
	}
}
