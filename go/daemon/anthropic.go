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
	id        string
	baseURL   string
	auth      *auth.Chain
	model     string
	client    *http.Client
	headers   map[string]string
	body      map[string]json.RawMessage
	overrides map[string]requestOverride
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

func newAnthropicCatalogProvider(id, baseURL, model string, chain *auth.Chain, headers map[string]string, body map[string]json.RawMessage, overrides map[string]requestOverride) modelrouter.Provider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	return &anthropicProvider{
		id:        id,
		baseURL:   strings.TrimRight(baseURL, "/"),
		auth:      chain,
		model:     model,
		client:    &http.Client{Timeout: providerHTTPTimeout},
		headers:   headers,
		body:      body,
		overrides: overrides,
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
	model, responseModel, override := a.resolveModel(req)
	messages := any([]map[string]string{{"role": "user", "content": req.Prompt}})
	if req.StablePrefix != "" {
		messages = []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": req.StablePrefix, "cache_control": map[string]string{"type": "ephemeral"}},
				{"type": "text", "text": req.VolatileSuffix},
			},
		}}
	}
	bodyMap := map[string]any{
		"model":      model,
		"max_tokens": 2048,
		"messages":   messages,
	}
	mergeRawBody(bodyMap, a.body)
	mergeRawBody(bodyMap, override.Body)
	body, _ := json.Marshal(bodyMap)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.baseURL, "/")+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	cred.Apply(httpReq.Header)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	applyHeaders(httpReq.Header, a.headers)
	applyHeaders(httpReq.Header, override.Headers)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", a.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(a.id, resp)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
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
		Provider:         a.Name(),
		Model:            responseModel,
		Text:             text,
		InputTokens:      out.Usage.InputTokens,
		OutputTokens:     out.Usage.OutputTokens,
		CacheReadTokens:  out.Usage.CacheReadTokens,
		CacheWriteTokens: out.Usage.CacheCreationTokens,
	}, nil
}

func (a *anthropicProvider) resolveModel(req modelrouter.Request) (apiModel, responseModel string, override requestOverride) {
	model := strings.TrimSpace(req.Model)
	if model == "" || model == "default" {
		model = a.model
	}
	responseModel = model
	if a.overrides != nil {
		if found, ok := a.overrides[model]; ok {
			override = found
			if strings.TrimSpace(found.Model) != "" {
				return strings.TrimSpace(found.Model), responseModel, found
			}
		}
	}
	return model, responseModel, override
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
