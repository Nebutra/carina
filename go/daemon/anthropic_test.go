package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

func TestAnthropicProviderUsesStablePrefixCacheControlAndParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("Anthropic request path = %q", r.URL.Path)
		}
		var body struct {
			MaxTokens int `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content []struct {
					Type         string            `json:"type"`
					Text         string            `json:"text"`
					CacheControl map[string]string `json:"cache_control"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.MaxTokens != agentMaxOutputTokens {
			t.Fatalf("max_tokens = %d, want %d", body.MaxTokens, agentMaxOutputTokens)
		}
		if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
			t.Fatalf("messages = %+v", body.Messages)
		}
		prefix, suffix := body.Messages[0].Content[0], body.Messages[0].Content[1]
		if prefix.Text != "stable" || prefix.CacheControl["type"] != "ephemeral" {
			t.Fatalf("stable prefix missing cache control: %+v", prefix)
		}
		if suffix.Text != "volatile" || len(suffix.CacheControl) != 0 {
			t.Fatalf("volatile suffix should not be cacheable: %+v", suffix)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":5,"cache_read_input_tokens":7}}`))
	}))
	defer srv.Close()

	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client(),
	}
	resp, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "default", Prompt: "stablevolatile", StablePrefix: "stable", VolatileSuffix: "volatile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.InputTokens != 2 || resp.OutputTokens != 3 || resp.CacheWriteTokens != 5 || resp.CacheReadTokens != 7 {
		t.Fatalf("usage = %+v", resp)
	}
}

func TestAnthropicProviderCachesEachPromptSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []struct {
					Type         string            `json:"type"`
					Text         string            `json:"text"`
					CacheControl map[string]string `json:"cache_control"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		blocks := body.Messages[0].Content
		if len(blocks) != 4 {
			t.Fatalf("want 3 cached sections + volatile, got %+v", blocks)
		}
		for i, name := range []string{"CONSTITUTION", "WORKSPACE", "CATALOG\n\nTASK: x\n\nTRANSCRIPT:\n"} {
			if blocks[i].Text != name || blocks[i].CacheControl["type"] != "ephemeral" {
				t.Fatalf("section %d = %+v", i, blocks[i])
			}
		}
		if blocks[3].Text != "turn\nGO" || len(blocks[3].CacheControl) != 0 {
			t.Fatalf("volatile = %+v", blocks[3])
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client(),
	}
	seg := buildPromptSegmentsFromLayers(promptLayers{
		Constitution: "CONSTITUTION",
		Workspace:    "WORKSPACE",
		Catalog:      "CATALOG",
	}, "x", "turn", "GO")
	_, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "default", Prompt: seg.full(), StablePrefix: seg.StablePrefix,
		StableSections: seg.CacheSections(), VolatileSuffix: seg.VolatileSuffix,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicEndpointNormalizesVersionExactlyOnce(t *testing.T) {
	tests := map[string]string{
		"https://api.example":                    "https://api.example/v1/messages",
		"https://api.example/":                   "https://api.example/v1/messages",
		"https://api.example/v1":                 "https://api.example/v1/messages",
		"https://api.example/prefix":             "https://api.example/prefix/v1/messages",
		"https://api.example/prefix/v1":          "https://api.example/prefix/v1/messages",
		"https://api.example/v1/messages":        "https://api.example/v1/messages",
		"https://api.example/prefix/v1/messages": "https://api.example/prefix/v1/messages",
	}
	for baseURL, want := range tests {
		if got := anthropicEndpoint(baseURL, "messages"); got != want {
			t.Errorf("anthropicEndpoint(%q) = %q, want %q", baseURL, got, want)
		}
	}
}

func TestAnthropicProviderClassifiesHTMLSuccessAsProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>control panel</body></html>"))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client(),
	}
	_, err := p.Complete(context.Background(), modelrouter.Request{Prompt: "hello"})
	if err == nil || !strings.Contains(err.Error(), "HTML document, expected JSON") {
		t.Fatalf("HTML response error = %v", err)
	}
	info := classifyProviderError(err)
	if info.Code != "provider_protocol_error" || info.Retryable || info.UserAction == "" {
		t.Fatalf("HTML response classification = %+v", info)
	}
}

func TestAnthropicProviderUsesBearerCredentialFromImportedProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer imported-token" || r.Header.Get("x-api-key") != "" {
			t.Fatalf("request path=%q auth=%q api-key=%q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetBearerToken("relay", "imported-token", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "relay", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("relay", nil, store, nil), client: srv.Client(),
	}
	resp, err := p.Complete(context.Background(), modelrouter.Request{Prompt: "hello"})
	if err != nil || resp.Text != "OK" {
		t.Fatalf("bearer completion = %+v, err=%v", resp, err)
	}
}
