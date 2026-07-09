package modelrouter

import (
	"context"
	"errors"
	"testing"
)

type fakeRerankProvider struct {
	name      string
	fail      bool
	calls     int
	lastModel string
	lastQuery string
	lastDocs  []string
}

func (f *fakeRerankProvider) Name() string { return f.name }

func (f *fakeRerankProvider) Rerank(_ context.Context, req RerankRequest) (*RerankResponse, error) {
	f.calls++
	f.lastModel = req.Model
	f.lastQuery = req.Query
	f.lastDocs = append([]string(nil), req.Documents...)
	if f.fail {
		return nil, errors.New("rerank provider down")
	}
	results := make([]RerankResult, len(req.Documents))
	for i := range req.Documents {
		// Reverse order, descending scores — a deterministic reorder.
		results[i] = RerankResult{Index: len(req.Documents) - 1 - i, Score: 1 - float64(i)/10}
	}
	model := req.Model
	if model == "" || model == "default" {
		model = "fake-rerank-model"
	}
	return &RerankResponse{Provider: f.name, Model: model, Results: results}, nil
}

func TestRerankFallbackToSecondRerankProvider(t *testing.T) {
	r := New()
	primary := &fakeRerankProvider{name: "primary-rr", fail: true}
	backup := &fakeRerankProvider{name: "backup-rr"}
	r.RegisterRerankProvider(primary)
	r.RegisterRerankProvider(backup)

	resp, err := r.Rerank(context.Background(), RerankRequest{
		Query: "find dispatch", Documents: []string{"d0", "d1"}, TopK: 2,
	})
	if err != nil {
		t.Fatalf("expected fallback success: %v", err)
	}
	if resp.Provider != "backup-rr" {
		t.Fatalf("expected backup provider, got %s", resp.Provider)
	}
	if primary.calls != 1 {
		t.Fatalf("primary must be tried first, calls=%d", primary.calls)
	}
	if len(resp.Results) != 2 || resp.Results[0].Index != 1 || resp.Results[1].Index != 0 {
		t.Fatalf("provider results must pass through in provider order, got %+v", resp.Results)
	}
	usage := r.UsageByProvider()
	if usage["backup-rr"].Requests != 1 {
		t.Fatalf("rerank usage not recorded: %+v", usage)
	}
}

func TestRerankTargetedProviderModel(t *testing.T) {
	r := New()
	voyage := &fakeRerankProvider{name: "voyage"}
	cohere := &fakeRerankProvider{name: "cohere"}
	r.RegisterRerankProvider(voyage)
	r.RegisterRerankProvider(cohere)

	resp, err := r.Rerank(context.Background(), RerankRequest{
		Model: "cohere/rerank-english-v3.0", Query: "q", Documents: []string{"a"}, TopK: 1,
	})
	if err != nil {
		t.Fatalf("targeted rerank: %v", err)
	}
	if resp.Provider != "cohere" {
		t.Fatalf("expected cohere, got %s", resp.Provider)
	}
	if cohere.lastModel != "rerank-english-v3.0" {
		t.Fatalf("targeted model suffix not preserved: %q", cohere.lastModel)
	}
	if voyage.calls != 0 {
		t.Fatalf("targeting must not try earlier providers, voyage calls=%d", voyage.calls)
	}
}

func TestRerankNoProvidersRegisteredErrors(t *testing.T) {
	r := New()
	if _, err := r.Rerank(context.Background(), RerankRequest{Query: "q", Documents: []string{"a"}}); err == nil {
		t.Fatal("a fresh router must error on Rerank")
	}
}

func TestHasRerankProviderNamedIsTheNoFallbackGuard(t *testing.T) {
	r := New()
	if r.HasRerankProvider() || r.HasRerankProviderNamed("voyage") {
		t.Fatal("a fresh router must report no rerank providers")
	}
	// Chat and embeddings registrations must not count.
	r.RegisterProvider(&fakeProvider{name: "chat-only"})
	r.RegisterEmbeddingsProvider(&fakeEmbeddingsProvider{name: "embed-only"})
	if r.HasRerankProvider() || r.HasRerankProviderNamed("chat-only") || r.HasRerankProviderNamed("embed-only") {
		t.Fatal("chat/embeddings providers must not enable the rerank stage")
	}
	r.RegisterRerankProvider(&fakeRerankProvider{name: "voyage"})
	if !r.HasRerankProvider() {
		t.Fatal("a registered rerank provider must flip HasRerankProvider")
	}
	if !r.HasRerankProviderNamed("voyage") {
		t.Fatal("HasRerankProviderNamed must match the exact registered name")
	}
	if r.HasRerankProviderNamed("cohere") {
		t.Fatal("HasRerankProviderNamed must not match unregistered names")
	}
}
