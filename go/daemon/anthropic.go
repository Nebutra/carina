package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
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
	label     string
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
		label:   "anthropic",
		baseURL: "https://api.anthropic.com/v1",
		auth:    chain,
		model:   envOr("ANTHROPIC_MODEL", "claude-fable-5"),
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func newAnthropicCatalogProvider(id, label, baseURL, model string, chain *auth.Chain, headers map[string]string, body map[string]json.RawMessage, overrides map[string]requestOverride) modelrouter.Provider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	return &anthropicProvider{
		id:        id,
		label:     label,
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

func (a *anthropicProvider) errorName() string {
	if label := strings.TrimSpace(a.label); label != "" {
		return label
	}
	return a.id
}

func (a *anthropicProvider) Complete(ctx context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	cred, ok := a.auth.Resolve()
	if !ok {
		return nil, fmt.Errorf("%s: credential not set", a.errorName())
	}
	if cred.Kind != auth.APIKey && cred.Kind != auth.Bearer && cred.Kind != auth.OAuth {
		return nil, fmt.Errorf("%s: supported credential not set", a.errorName())
	}
	model, responseModel, override := a.resolveModel(req)
	messages := any([]map[string]string{{"role": "user", "content": req.Prompt}})
	if blocks, ok := anthropicUserBlocks(req); ok {
		messages = []map[string]any{{"role": "user", "content": blocks}}
	}
	bodyMap := map[string]any{
		"model":      model,
		"max_tokens": agentMaxOutputTokens,
		"messages":   messages,
	}
	mergeRawBody(bodyMap, a.body)
	mergeRawBody(bodyMap, override.Body)
	attachAnthropicTools(bodyMap, req.Tools)
	effectiveEffort, err := validateReasoningEffort(nativeReasoningEffortSpec(a.id, model), req.ReasoningEffort)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", a.errorName(), model, err)
	}
	if effectiveEffort != "" {
		bodyMap["thinking"] = map[string]any{"type": "adaptive"}
		bodyMap["output_config"] = map[string]any{"effort": effectiveEffort}
	}
	body, _ := json.Marshal(bodyMap)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint(a.baseURL, "messages"), bytes.NewReader(body))
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
		return nil, fmt.Errorf("%s: request: %w", a.errorName(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(a.errorName(), resp)
	}
	var out struct {
		Content []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Text  string         `json:"text"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := decodeProviderJSON(a.errorName(), resp, &out); err != nil {
		return nil, err
	}
	text := ""
	var calls []modelrouter.ToolCall
	for _, c := range out.Content {
		text += c.Text
		if c.Type == "tool_use" && c.Name != "" {
			calls = append(calls, modelrouter.ToolCall{
				ID: c.ID, Name: c.Name, Arguments: encodeJSONArguments(c.Input),
			})
		}
	}
	return &modelrouter.Response{
		Provider:                 a.Name(),
		Model:                    responseModel,
		Text:                     text,
		InputTokens:              out.Usage.InputTokens,
		OutputTokens:             out.Usage.OutputTokens,
		CacheReadTokens:          out.Usage.CacheReadTokens,
		CacheWriteTokens:         out.Usage.CacheCreationTokens,
		EffectiveReasoningEffort: effectiveEffort,
		ToolCalls:                calls,
	}, nil
}

// anthropicUserBlocks splits the stuffed user prompt into cacheable text
// sections plus a volatile suffix. Image blocks stay after text so they do
// not move a cache breakpoint. Grok CLI never calls this helper.
func anthropicUserBlocks(req modelrouter.Request) ([]map[string]any, bool) {
	sections := append([]string(nil), req.StableSections...)
	if len(sections) == 0 && req.StablePrefix != "" {
		sections = []string{req.StablePrefix}
	}
	if len(sections) == 0 && len(req.Media) == 0 {
		return nil, false
	}
	// Anthropic Messages prompt caching allows at most four cache_control
	// breakpoints. A–D take them first so Identity/Mode/Protocol/Tools stay
	// independently cacheable; leftover workspace/catalog ride as plain text.
	const maxAnthropicCacheBreakpoints = 4
	blocks := make([]map[string]any, 0, len(sections)+1+len(req.Media))
	cached := 0
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}
		block := map[string]any{
			"type": "text",
			"text": section,
		}
		if cached < maxAnthropicCacheBreakpoints {
			block["cache_control"] = map[string]string{"type": "ephemeral"}
			cached++
		}
		blocks = append(blocks, block)
	}
	if len(blocks) > 0 || req.VolatileSuffix != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": req.VolatileSuffix})
	} else if req.Prompt != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": req.Prompt})
	}
	blocks = append(blocks, anthropicImageBlocks(req.Media)...)
	return blocks, len(blocks) > 0
}

// anthropicImageBlocks encodes request media as Anthropic Messages API image
// content blocks (base64 source). Empty media yields nil (no blocks).
func anthropicImageBlocks(media []modelrouter.MediaPart) []map[string]any {
	if len(media) == 0 {
		return nil
	}
	blocks := make([]map[string]any, 0, len(media))
	for _, m := range media {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": m.MediaType,
				"data":       base64.StdEncoding.EncodeToString(m.Data),
			},
		})
	}
	return blocks
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
