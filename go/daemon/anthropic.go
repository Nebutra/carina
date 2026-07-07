package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

// anthropicProvider calls the Anthropic Messages API. It is registered
// ahead of the mock provider, so the router uses it when ANTHROPIC_API_KEY
// is set and transparently falls back to mock otherwise (PRD §8.6:
// provider fallback).
type anthropicProvider struct {
	id      string
	baseURL string
	auth    *auth.Chain
	model   string
	client  *http.Client
}

// NewAnthropicProvider uses the daemon auth chain and ANTHROPIC_MODEL.
func NewAnthropicProvider(chain *auth.Chain) modelrouter.Provider {
	return &anthropicProvider{
		id:      "anthropic",
		baseURL: "https://api.anthropic.com/v1",
		auth:    chain,
		model:   envOr("ANTHROPIC_MODEL", "claude-fable-5"),
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func newAnthropicCatalogProvider(id, baseURL, model string, chain *auth.Chain) modelrouter.Provider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	return &anthropicProvider{
		id:      id,
		baseURL: strings.TrimRight(baseURL, "/"),
		auth:    chain,
		model:   model,
		client:  &http.Client{Timeout: providerHTTPTimeout},
	}
}

func (a *anthropicProvider) Name() string { return a.id }

func (a *anthropicProvider) Complete(ctx context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	cred, ok := a.auth.Resolve()
	if !ok {
		return nil, fmt.Errorf("%s: credential not set", a.id)
	}
	if cred.Kind != auth.APIKey {
		return nil, fmt.Errorf("%s: api key credential not set", a.id)
	}
	model := req.Model
	if model == "" || model == "default" {
		model = a.model
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 2048,
		"messages":   []map[string]string{{"role": "user", "content": req.Prompt}},
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.baseURL, "/")+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	cred.Apply(httpReq.Header)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", a.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d", a.id, resp.StatusCode)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", a.id, err)
	}
	text := ""
	for _, c := range out.Content {
		text += c.Text
	}
	return &modelrouter.Response{
		Provider:     a.Name(),
		Model:        model,
		Text:         text,
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
