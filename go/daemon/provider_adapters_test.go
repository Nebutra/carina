package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

func TestOpenAIChatProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-chat" {
			t.Fatalf("authorization header = %q", got)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "mixtral" || len(body.Messages) != 1 || body.Messages[0].Content != "hello" {
			t.Fatalf("bad body: %+v", body)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"chat ok"}}],"usage":{"prompt_tokens":7,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":5}}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openrouter", "sk-chat", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openrouter", baseURL: srv.URL + "/v1", defaultModel: "default-model",
		auth: auth.ProviderChain("openrouter", nil, store, nil), client: srv.Client(),
	}}

	resp, err := p.Complete(context.Background(), modelrouter.Request{Model: "mixtral", Prompt: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Provider != "openrouter" || resp.Text != "chat ok" || resp.InputTokens != 2 || resp.OutputTokens != 3 || resp.CacheReadTokens != 5 {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestOpenAIChatProviderAppliesModelOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Mode"); got != "fast" {
			t.Fatalf("mode header = %q", got)
		}
		var body struct {
			Model           string `json:"model"`
			MaxTokens       int    `json:"max_tokens"`
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "xai/grok-4" || body.MaxTokens != 77 || body.ReasoningEffort != "low" {
			t.Fatalf("override not applied: %+v", body)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"override ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("requesty", "sk-chat", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "requesty", baseURL: srv.URL + "/v1", defaultModel: "xai/grok-4-fast",
		auth: auth.ProviderChain("requesty", nil, store, nil), client: srv.Client(),
		overrides: map[string]requestOverride{
			"xai/grok-4-fast": {
				Model:   "xai/grok-4",
				Headers: map[string]string{"X-Mode": "fast"},
				Body: map[string]json.RawMessage{
					"max_tokens":       json.RawMessage(`77`),
					"reasoning_effort": json.RawMessage(`"low"`),
				},
			},
		},
	}}

	resp, err := p.Complete(context.Background(), modelrouter.Request{Model: "default", Prompt: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Model != "xai/grok-4-fast" || resp.Text != "override ok" {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestOpenAIResponsesProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-openai" {
			t.Fatalf("authorization header = %q", got)
		}
		var body struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "gpt-5" || body.Input != "hello" {
			t.Fatalf("bad body: %+v", body)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"output_text":"responses ok","usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":6}}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-openai", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL + "/v1", defaultModel: "gpt-5",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}, responses: true}

	resp, err := p.Complete(context.Background(), modelrouter.Request{Model: "default", Prompt: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Text != "responses ok" || resp.Model != "gpt-5" || resp.InputTokens != 4 || resp.OutputTokens != 5 || resp.CacheReadTokens != 6 {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestGeminiProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-pro:generateContent" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "sk-gemini" {
			t.Fatalf("query key = %q", got)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"gemini ok"}]}}],"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":7}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("google", "sk-gemini", nil); err != nil {
		t.Fatal(err)
	}
	p := &geminiProvider{providerBase: providerBase{
		id: "google", baseURL: srv.URL + "/v1beta", defaultModel: "gemini-pro",
		auth: auth.ProviderChain("google", nil, store, nil), client: srv.Client(),
	}}

	resp, err := p.Complete(context.Background(), modelrouter.Request{Model: "default", Prompt: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Text != "gemini ok" || resp.InputTokens != 6 || resp.OutputTokens != 7 {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestCatalogRegistrationTargetsOpenAICompatibleProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-requesty" {
			t.Fatalf("authorization header = %q", got)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"catalog ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("requesty", "sk-requesty", nil); err != nil {
		t.Fatal(err)
	}
	cat := provider.Catalog{
		"requesty": {
			ID:     "requesty",
			Name:   "Requesty",
			API:    srv.URL + "/v1",
			NPM:    "@ai-sdk/openai-compatible",
			Env:    []string{"REQUESTY_API_KEY"},
			Models: map[string]provider.Model{"xai/grok-4": {ID: "xai/grok-4", Name: "Grok 4"}},
		},
	}
	router := modelrouter.New()
	registerProviders(router, false, store, cat)

	resp, err := router.Complete(context.Background(), modelrouter.Request{Model: "requesty/xai/grok-4", Prompt: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Provider != "requesty" || resp.Model != "xai/grok-4" || resp.Text != "catalog ok" {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestCatalogRegistrationAppliesProviderQuirkAndModeOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("HTTP-Referer"); got != "https://github.com/Nebutra/carina" {
			t.Fatalf("referer header = %q", got)
		}
		if got := r.Header.Get("X-Title"); got != "Carina" {
			t.Fatalf("title header = %q", got)
		}
		if got := r.Header.Get("X-Mode"); got != "fast" {
			t.Fatalf("mode header = %q", got)
		}
		var body struct {
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "openai/gpt-5" || body.ReasoningEffort != "minimal" {
			t.Fatalf("bad body: %+v", body)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"mode ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openrouter", "sk-openrouter", nil); err != nil {
		t.Fatal(err)
	}
	cat := provider.Catalog{
		"openrouter": {
			ID:   "openrouter",
			Name: "OpenRouter",
			API:  srv.URL + "/v1",
			NPM:  "@openrouter/ai-sdk-provider",
			Env:  []string{"OPENROUTER_API_KEY"},
			Models: map[string]provider.Model{
				"openai/gpt-5": {
					ID:   "openai/gpt-5",
					Name: "GPT-5",
					Experimental: &provider.ModelExperimental{Modes: map[string]provider.ModelMode{
						"fast": {Provider: &provider.ModelProviderOverride{
							Headers: map[string]string{"X-Mode": "fast"},
							Body: map[string]json.RawMessage{
								"reasoning_effort": json.RawMessage(`"minimal"`),
							},
						}},
					}},
				},
			},
		},
	}
	router := modelrouter.New()
	registerProviders(router, false, store, cat)

	resp, err := router.Complete(context.Background(), modelrouter.Request{Model: "openrouter/openai/gpt-5-fast", Prompt: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Provider != "openrouter" || resp.Model != "openai/gpt-5-fast" || resp.Text != "mode ok" {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestProviderStatusErrorCarriesRetryAfter(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After-Ms": []string{"1250"}}}
	err := statusError("openai", resp)
	var retryable retryAfterProvider
	if !errors.As(err, &retryable) {
		t.Fatal("status error should expose retry-after")
	}
	delay, ok := retryable.RetryAfter()
	if !ok || delay != 1250*time.Millisecond {
		t.Fatalf("delay=%s ok=%v", delay, ok)
	}
}

func TestProviderStatusErrorClassification(t *testing.T) {
	tests := []struct {
		status    int
		category  string
		retryable bool
	}{
		{400, "invalid_input", false}, {401, "authentication", false}, {402, "rate_limit", false}, {403, "permission", false},
		{429, "rate_limit", true}, {503, "unavailable", true},
	}
	for _, test := range tests {
		info := providerStatusError{provider: "p", status: test.status}.ProviderError()
		if info.Category != test.category || info.Retryable != test.retryable {
			t.Errorf("status %d: %+v", test.status, info)
		}
	}
}

func TestMissingCredentialIsActionableAndNotRetryable(t *testing.T) {
	_, _, err := (&providerBase{id: "openai"}).credential()
	info := classifyProviderError(err)
	if info.Category != "authentication" || info.Retryable || info.UserAction == "" {
		t.Fatalf("classification=%+v", info)
	}
}

func TestChooseCatalogModelScoresTextReasoningModels(t *testing.T) {
	got := chooseCatalogModel(map[string]provider.Model{
		"new-embedding": {
			ID:          "new-embedding",
			Name:        "Newest Embedding",
			ReleaseDate: "2026-01-01",
			Modalities:  &provider.Modalities{Input: []string{"text"}, Output: []string{}},
		},
		"older-chat": {
			ID:          "older-chat",
			Name:        "Older Chat",
			ReleaseDate: "2025-01-01",
			Reasoning:   true,
			ToolCall:    true,
			Limit:       provider.ModelLimit{Context: 200000, Output: 16000},
			Modalities:  &provider.Modalities{Input: []string{"text"}, Output: []string{"text"}},
		},
	})
	if got != "older-chat" {
		t.Fatalf("expected text chat model, got %q", got)
	}
}

func TestExpandEnvStrict(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	if got, ok := expandEnvStrict("https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/ai/v1"); !ok || got != "https://api.cloudflare.com/client/v4/accounts/acct/ai/v1" {
		t.Fatalf("expanded = %q ok=%v", got, ok)
	}
	if _, ok := expandEnvStrict("https://${MISSING_HOST}/v1"); ok {
		t.Fatal("missing env ref should not expand successfully")
	}
}

func testAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
