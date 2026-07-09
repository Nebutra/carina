package modelrouter

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeEmbeddingsProvider struct {
	name       string
	fail       bool
	calls      int
	lastInputs []string
	lastModel  string
}

func (f *fakeEmbeddingsProvider) Name() string { return f.name }
func (f *fakeEmbeddingsProvider) Embed(_ context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error) {
	f.calls++
	f.lastInputs = append([]string(nil), req.Inputs...)
	f.lastModel = req.Model
	if f.fail {
		return nil, errors.New("embeddings provider down")
	}
	vectors := make([][]float32, len(req.Inputs))
	tokens := 0
	for i, input := range req.Inputs {
		vectors[i] = []float32{float32(len(input)), 1}
		tokens += len(input)
	}
	model := req.Model
	if model == "" || model == "default" {
		model = "fake-embed-model"
	}
	return &EmbeddingsResponse{Provider: f.name, Model: model, Vectors: vectors, InputTokens: tokens}, nil
}

func TestEmbedFallbackToSecondEmbeddingsProvider(t *testing.T) {
	r := New()
	primary := &fakeEmbeddingsProvider{name: "primary-embed", fail: true}
	backup := &fakeEmbeddingsProvider{name: "backup-embed"}
	r.RegisterEmbeddingsProvider(primary)
	r.RegisterEmbeddingsProvider(backup)

	resp, err := r.Embed(context.Background(), EmbeddingsRequest{Inputs: []string{"aa", "bbb"}})
	if err != nil {
		t.Fatalf("expected fallback success: %v", err)
	}
	if resp.Provider != "backup-embed" {
		t.Fatalf("expected backup provider, got %s", resp.Provider)
	}
	if primary.calls != 1 {
		t.Fatalf("primary must be tried first, calls=%d", primary.calls)
	}
	if len(resp.Vectors) != 2 {
		t.Fatalf("one vector per input, got %d", len(resp.Vectors))
	}
	usage := r.UsageByProvider()
	if usage["backup-embed"].Requests != 1 {
		t.Fatalf("embed usage not recorded: %+v", usage)
	}
	if usage["backup-embed"].InputTokens != 5 {
		t.Fatalf("input tokens must be accounted, got %+v", usage["backup-embed"])
	}
}

func TestEmbedTargetedProviderModel(t *testing.T) {
	r := New()
	openai := &fakeEmbeddingsProvider{name: "openai"}
	voyage := &fakeEmbeddingsProvider{name: "voyage"}
	r.RegisterEmbeddingsProvider(openai)
	r.RegisterEmbeddingsProvider(voyage)

	resp, err := r.Embed(context.Background(), EmbeddingsRequest{
		Model:  "voyage/voyage-code-3",
		Inputs: []string{"func main() {}"},
	})
	if err != nil {
		t.Fatalf("targeted embed: %v", err)
	}
	if resp.Provider != "voyage" {
		t.Fatalf("expected voyage, got %s", resp.Provider)
	}
	if resp.Model != "voyage-code-3" {
		t.Fatalf("targeted model suffix not preserved: %q", resp.Model)
	}
	if openai.calls != 0 {
		t.Fatalf("targeting must not try earlier providers, openai calls=%d", openai.calls)
	}
}

func TestEmbedUnknownSlashModelFallsBackNormally(t *testing.T) {
	r := New()
	r.RegisterEmbeddingsProvider(&fakeEmbeddingsProvider{name: "openai"})

	resp, err := r.Embed(context.Background(), EmbeddingsRequest{
		Model:  "meta-llama/embed-x",
		Inputs: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("unknown slash prefix should remain a model id: %v", err)
	}
	if resp.Model != "meta-llama/embed-x" {
		t.Fatalf("slash model should be unchanged: %q", resp.Model)
	}
}

func TestHasEmbeddingsProviderIsTheDegradeSignal(t *testing.T) {
	r := New()
	if r.HasEmbeddingsProvider() {
		t.Fatal("a fresh router must report no embeddings providers")
	}
	// Chat providers must not count: only embeddings registrations flip it.
	r.RegisterProvider(&fakeProvider{name: "chat-only"})
	if r.HasEmbeddingsProvider() {
		t.Fatal("chat providers must not enable the semantic layer")
	}
	r.RegisterEmbeddingsProvider(&fakeEmbeddingsProvider{name: "embed"})
	if !r.HasEmbeddingsProvider() {
		t.Fatal("a registered embeddings provider must be detected")
	}
}

func TestEmbedWithNoProvidersReportsRegistryEmpty(t *testing.T) {
	r := New()
	_, err := r.Embed(context.Background(), EmbeddingsRequest{Inputs: []string{"x"}})
	if err == nil {
		t.Fatal("empty embeddings registry must error")
	}
	if !strings.Contains(err.Error(), "no embeddings providers") {
		t.Fatalf("error must name the empty registry, got: %v", err)
	}
}

func TestEmbedAllProvidersFailingJoinsErrors(t *testing.T) {
	r := New()
	r.RegisterEmbeddingsProvider(&fakeEmbeddingsProvider{name: "a-embed", fail: true})
	r.RegisterEmbeddingsProvider(&fakeEmbeddingsProvider{name: "b-embed", fail: true})
	_, err := r.Embed(context.Background(), EmbeddingsRequest{Inputs: []string{"x"}})
	if err == nil {
		t.Fatal("all-failing embeddings registry must error")
	}
	if !strings.Contains(err.Error(), "a-embed") || !strings.Contains(err.Error(), "b-embed") {
		t.Fatalf("joined error must name each failed provider, got: %v", err)
	}
}
