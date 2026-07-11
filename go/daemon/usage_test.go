package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func quarantineFiles(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestUsageStoreQuarantinesFutureVersionInsteadOfOverwriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model-usage.json")
	future := `{"version": 2, "records": [{"session_id": "s", "task_id": "t", "provider": "p", "model": "m", "requests": 1}]}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newUsageStore(dir)
	if len(s.records) != 0 {
		t.Fatalf("future-version records must not be trusted: %+v", s.records)
	}
	// The destroy path: the very next record() used to rewrite the file as v1
	// over the future-version original.
	if err := s.record("sess", "task", ModelUsage{Provider: "anthropic", Model: "claude", InputTokens: 1}); err != nil {
		t.Fatal(err)
	}

	moved := quarantineFiles(t, path+".v2.*.quarantine")
	if len(moved) != 1 {
		t.Fatalf("want exactly one quarantine file, got %v", moved)
	}
	kept, err := os.ReadFile(moved[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != future {
		t.Fatalf("original future-version bytes destroyed: %s", kept)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version": 1`) {
		t.Fatalf("rewritten store must be stamped v1: %s", raw)
	}
}

func TestUsageStoreQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model-usage.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "records": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newUsageStore(dir)
	if len(s.records) != 0 {
		t.Fatalf("corrupt file must load empty: %+v", s.records)
	}
	if moved := quarantineFiles(t, path+".v*.quarantine"); len(moved) != 1 {
		t.Fatalf("corrupt file must be quarantined, got %v", moved)
	}
}

func TestUsageStoreLoadsLegacyUnstampedEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model-usage.json")
	legacy := `{"records": [{"session_id": "s", "task_id": "t", "provider": "anthropic", "model": "claude", "input_tokens": 5, "requests": 1}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newUsageStore(dir)
	if len(s.records) != 1 {
		t.Fatalf("legacy unstamped envelope must load as v1: %+v", s.records)
	}
	if moved := quarantineFiles(t, path+".v*.quarantine"); len(moved) != 0 {
		t.Fatalf("legacy file must never be quarantined, got %v", moved)
	}
}
