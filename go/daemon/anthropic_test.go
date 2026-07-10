package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

func TestAnthropicProviderUsesStablePrefixCacheControlAndParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
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
