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
	bodyMap := map[string]any{
		"model":      model,
		"max_tokens": agentMaxOutputTokens,
		"messages":   anthropicMessages(req),
	}
	if system := anthropicSystemBlocks(req); len(system) > 0 {
		bodyMap["system"] = system
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

// Anthropic Messages prompt caching allows at most four cache_control
// breakpoints. A–D take them on `system`. Workspace/catalog sit after the
// boundary as uncached system text. TASK/transcript stay on the user turn.
const maxAnthropicCacheBreakpoints = 4

func anthropicTextBlock(text string, cache bool) map[string]any {
	block := map[string]any{"type": "text", "text": text}
	if cache {
		block["cache_control"] = map[string]string{"type": "ephemeral"}
	}
	return block
}

func anthropicSystemTexts(req modelrouter.Request) (system, dynamic []string) {
	if len(req.SystemSections) > 0 || len(req.DynamicSections) > 0 {
		return req.SystemSections, req.DynamicSections
	}
	if strings.TrimSpace(req.StablePrefix) != "" {
		return []string{req.StablePrefix}, nil
	}
	return nil, nil
}

func anthropicSystemBlocks(req modelrouter.Request) []map[string]any {
	system, dynamic := anthropicSystemTexts(req)
	if len(system) == 0 && len(dynamic) == 0 {
		return nil
	}
	blocks := make([]map[string]any, 0, len(system)+len(dynamic))
	cached := 0
	for _, text := range system {
		if strings.TrimSpace(text) == "" {
			continue
		}
		cache := cached < maxAnthropicCacheBreakpoints
		if cache {
			cached++
		}
		blocks = append(blocks, anthropicTextBlock(text, cache))
	}
	for _, text := range dynamic {
		if strings.TrimSpace(text) == "" {
			continue
		}
		blocks = append(blocks, anthropicTextBlock(text, false))
	}
	return blocks
}

func anthropicMessages(req modelrouter.Request) any {
	if blocks, ok := anthropicUserBlocks(req); ok {
		return []map[string]any{{"role": "user", "content": blocks}}
	}
	return []map[string]string{{"role": "user", "content": req.Prompt}}
}

// anthropicUserBlocks is the volatile user turn: TASK, transcript, closing,
// then image blocks. Constitution does not belong here.
func anthropicUserBlocks(req modelrouter.Request) ([]map[string]any, bool) {
	system := anthropicSystemBlocks(req)
	var blocks []map[string]any
	if strings.TrimSpace(req.VolatileSuffix) != "" {
		blocks = append(blocks, anthropicTextBlock(req.VolatileSuffix, false))
	} else if len(system) == 0 && strings.TrimSpace(req.Prompt) != "" {
		return nil, false
	}
	blocks = append(blocks, anthropicImageBlocks(req.Media)...)
	if len(blocks) == 0 && len(system) > 0 {
		// Anthropic requires a user message; keep a minimal turn if the
		// caller supplied system without a suffix (tests always send both).
		return []map[string]any{anthropicTextBlock(" ", false)}, true
	}
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
