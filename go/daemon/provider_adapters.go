package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

const providerHTTPTimeout = 120 * time.Second

// mediaDataURI encodes one request media part as a data: URI, the shape the
// OpenAI-style chat (image_url) and responses (input_image) APIs accept.
func mediaDataURI(m modelrouter.MediaPart) string {
	return "data:" + m.MediaType + ";base64," + base64.StdEncoding.EncodeToString(m.Data)
}

type providerBase struct {
	id           string
	baseURL      string
	defaultModel string
	auth         *auth.Chain
	client       *http.Client
	noAuth       bool
	headers      map[string]string
	body         map[string]json.RawMessage
	overrides    map[string]requestOverride
}

type requestOverride struct {
	Model   string
	Headers map[string]string
	Body    map[string]json.RawMessage
}

type providerCredentialError struct{ provider string }

func (e providerCredentialError) Error() string { return e.provider + ": credential not set" }
func (e providerCredentialError) ProviderError() providerErrorInfo {
	return providerErrorInfo{Code: "provider_credential_missing", Category: "authentication", Provider: e.provider, UserAction: "configure a provider credential", Retryable: false}
}

func (p *providerBase) name() string { return p.id }

func (p *providerBase) model(req modelrouter.Request) string {
	apiModel, _, _ := p.resolveModel(req)
	return apiModel
}

func (p *providerBase) resolveModel(req modelrouter.Request) (apiModel, responseModel string, override requestOverride) {
	model := strings.TrimSpace(req.Model)
	if model == "" || model == "default" {
		model = p.defaultModel
	}
	responseModel = model
	if p.overrides != nil {
		if found, ok := p.overrides[model]; ok {
			override = found
			if strings.TrimSpace(found.Model) != "" {
				return strings.TrimSpace(found.Model), responseModel, found
			}
		}
	}
	return model, responseModel, override
}

func (p *providerBase) credential() (auth.Credential, bool, error) {
	if p.auth == nil {
		if p.noAuth {
			return auth.Credential{}, false, nil
		}
		return auth.Credential{}, false, providerCredentialError{provider: p.id}
	}
	cred, ok := p.auth.Resolve()
	if !ok || strings.TrimSpace(cred.Value) == "" {
		if p.noAuth {
			return auth.Credential{}, false, nil
		}
		return auth.Credential{}, false, providerCredentialError{provider: p.id}
	}
	return cred, true, nil
}

func (p *providerBase) httpClient() *http.Client {
	if p.client != nil {
		return p.client
	}
	return &http.Client{Timeout: providerHTTPTimeout}
}

func (p *providerBase) endpoint(path string) string {
	base := strings.TrimRight(p.baseURL, "/")
	path = strings.TrimLeft(path, "/")
	if path == "" {
		return base
	}
	if strings.HasSuffix(base, "/"+path) {
		return base
	}
	return base + "/" + path
}

func (p *providerBase) applyExtraHeaders(h http.Header) {
	for k, v := range p.headers {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			h.Set(k, v)
		}
	}
}

func applyHeaders(h http.Header, headers map[string]string) {
	for k, v := range headers {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			h.Set(k, v)
		}
	}
}

func mergeRawBody(dst map[string]any, body map[string]json.RawMessage) {
	for k, raw := range body {
		if strings.TrimSpace(k) == "" || len(raw) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			dst[k] = v
		}
	}
}

type providerStatusError struct {
	provider  string
	status    int
	retry     time.Duration
	requestID string
}

func (e providerStatusError) Error() string {
	if e.retry > 0 {
		return fmt.Sprintf("%s: status %d; retry after %s", e.provider, e.status, e.retry)
	}
	return fmt.Sprintf("%s: status %d", e.provider, e.status)
}

func (e providerStatusError) RetryAfter() (time.Duration, bool) {
	return e.retry, e.retry > 0
}

func (e providerStatusError) ProviderError() providerErrorInfo {
	info := providerErrorInfo{Code: "provider_http_error", Category: "internal", Retryable: false, Provider: e.provider, HTTPStatus: e.status, CorrelationID: e.requestID}
	switch {
	case e.status == http.StatusPaymentRequired:
		info.Code, info.Category, info.UserAction = "provider_quota_exhausted", "rate_limit", "increase quota or choose another provider"
	case e.status == http.StatusUnauthorized:
		info.Code, info.Category, info.UserAction = "provider_authentication_failed", "authentication", "check the provider credential"
	case e.status == http.StatusForbidden:
		info.Code, info.Category, info.UserAction = "provider_permission_denied", "permission", "check provider account and model permissions"
	case e.status == http.StatusTooManyRequests:
		info.Code, info.Category, info.Retryable, info.UserAction = "provider_rate_limited", "rate_limit", true, "wait or choose another provider"
	case e.status == http.StatusRequestTimeout || e.status == http.StatusTooEarly:
		info.Code, info.Category, info.Retryable = "provider_timeout", "timeout", true
	case e.status == http.StatusConflict:
		info.Code, info.Category, info.Retryable = "provider_conflict", "conflict", true
	case e.status >= 500:
		info.Code, info.Category, info.Retryable = "provider_unavailable", "unavailable", true
	case e.status >= 400:
		info.Code, info.Category, info.UserAction = "provider_invalid_request", "invalid_input", "check the model and request configuration"
	}
	return info
}

func statusError(provider string, resp *http.Response) error {
	requestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	if requestID == "" {
		requestID = strings.TrimSpace(resp.Header.Get("request-id"))
	}
	if len(requestID) > 128 {
		requestID = requestID[:128]
	}
	return providerStatusError{provider: provider, status: resp.StatusCode, retry: retryAfter(resp.Header, time.Now()), requestID: requestID}
}

func retryAfter(h http.Header, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("retry-after-ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	if v := strings.TrimSpace(h.Get("retry-after")); v != "" {
		if seconds, err := strconv.ParseFloat(v, 64); err == nil && seconds > 0 {
			return time.Duration(seconds * float64(time.Second))
		}
		if t, err := http.ParseTime(v); err == nil && t.After(now) {
			return t.Sub(now)
		}
	}
	return 0
}

type openAIProvider struct {
	providerBase
	responses bool
}

func (o *openAIProvider) Name() string { return o.name() }

func (o *openAIProvider) Complete(ctx context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	if o.responses {
		return o.completeResponses(ctx, req)
	}
	return o.completeChat(ctx, req)
}

func (o *openAIProvider) completeChat(ctx context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	cred, hasCred, err := o.credential()
	if err != nil {
		return nil, err
	}
	model, responseModel, override := o.resolveModel(req)
	content := any(req.Prompt)
	if len(req.Media) > 0 {
		parts := []map[string]any{{"type": "text", "text": req.Prompt}}
		for _, m := range req.Media {
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]string{"url": mediaDataURI(m)},
			})
		}
		content = parts
	}
	bodyMap := map[string]any{
		"model":      model,
		"max_tokens": 2048,
		"messages":   []map[string]any{{"role": "user", "content": content}},
	}
	mergeRawBody(bodyMap, o.body)
	mergeRawBody(bodyMap, override.Body)
	body, _ := json.Marshal(bodyMap)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint("chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	if hasCred {
		httpReq.Header.Set("Authorization", "Bearer "+cred.Value)
	}
	o.applyExtraHeaders(httpReq.Header)
	applyHeaders(httpReq.Header, override.Headers)
	resp, err := o.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", o.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError(o.id, resp)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", o.id, err)
	}
	text := ""
	for _, c := range out.Choices {
		text += textFromRaw(c.Message.Content)
	}
	if text == "" {
		return nil, fmt.Errorf("%s: empty response", o.id)
	}
	cached := clampCachedTokens(out.Usage.PromptDetails.CachedTokens, out.Usage.PromptTokens)
	return &modelrouter.Response{
		Provider:        o.Name(),
		Model:           responseModel,
		Text:            text,
		InputTokens:     out.Usage.PromptTokens - cached,
		OutputTokens:    out.Usage.CompletionTokens,
		CacheReadTokens: cached,
	}, nil
}

func (o *openAIProvider) completeResponses(ctx context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	cred, hasCred, err := o.credential()
	if err != nil {
		return nil, err
	}
	model, responseModel, override := o.resolveModel(req)
	input := any(req.Prompt)
	if len(req.Media) > 0 {
		parts := []map[string]any{{"type": "input_text", "text": req.Prompt}}
		for _, m := range req.Media {
			parts = append(parts, map[string]any{
				"type":      "input_image",
				"image_url": mediaDataURI(m),
			})
		}
		input = []map[string]any{{"role": "user", "content": parts}}
	}
	bodyMap := map[string]any{
		"model":             model,
		"input":             input,
		"max_output_tokens": 2048,
	}
	mergeRawBody(bodyMap, o.body)
	mergeRawBody(bodyMap, override.Body)
	body, _ := json.Marshal(bodyMap)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint("responses"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	if hasCred {
		httpReq.Header.Set("Authorization", "Bearer "+cred.Value)
	}
	o.applyExtraHeaders(httpReq.Header)
	applyHeaders(httpReq.Header, override.Headers)
	resp, err := o.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", o.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError(o.id, resp)
	}
	var out struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			InputDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", o.id, err)
	}
	text := out.OutputText
	if text == "" {
		for _, item := range out.Output {
			for _, part := range item.Content {
				text += part.Text
			}
		}
	}
	if text == "" {
		return nil, fmt.Errorf("%s: empty response", o.id)
	}
	cached := clampCachedTokens(out.Usage.InputDetails.CachedTokens, out.Usage.InputTokens)
	return &modelrouter.Response{
		Provider:        o.Name(),
		Model:           responseModel,
		Text:            text,
		InputTokens:     out.Usage.InputTokens - cached,
		OutputTokens:    out.Usage.OutputTokens,
		CacheReadTokens: cached,
	}, nil
}

func clampCachedTokens(cached, total int) int {
	if cached < 0 {
		return 0
	}
	if cached > total {
		return total
	}
	return cached
}

type geminiProvider struct{ providerBase }

func (g *geminiProvider) Name() string { return g.name() }

func (g *geminiProvider) Complete(ctx context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	cred, hasCred, err := g.credential()
	if err != nil {
		return nil, err
	}
	model, responseModel, override := g.resolveModel(req)
	endpoint := g.endpoint("models/" + url.PathEscape(model) + ":generateContent")
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	parts := []map[string]any{{"text": req.Prompt}}
	for _, m := range req.Media {
		parts = append(parts, map[string]any{
			"inline_data": map[string]string{
				"mime_type": m.MediaType,
				"data":      base64.StdEncoding.EncodeToString(m.Data),
			},
		})
	}
	bodyMap := map[string]any{
		"contents": []map[string]any{{
			"parts": parts,
		}},
		"generationConfig": map[string]any{"maxOutputTokens": 2048},
	}
	mergeRawBody(bodyMap, g.body)
	mergeRawBody(bodyMap, override.Body)
	body, _ := json.Marshal(bodyMap)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	if hasCred {
		if cred.Kind == auth.OAuth {
			httpReq.Header.Set("Authorization", "Bearer "+cred.Value)
		} else {
			q := httpReq.URL.Query()
			q.Set("key", cred.Value)
			httpReq.URL.RawQuery = q.Encode()
		}
	}
	g.applyExtraHeaders(httpReq.Header)
	applyHeaders(httpReq.Header, override.Headers)
	resp, err := g.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", g.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError(g.id, resp)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", g.id, err)
	}
	text := ""
	for _, c := range out.Candidates {
		for _, part := range c.Content.Parts {
			text += part.Text
		}
	}
	if text == "" {
		return nil, fmt.Errorf("%s: empty response", g.id)
	}
	return &modelrouter.Response{
		Provider:     g.Name(),
		Model:        responseModel,
		Text:         text,
		InputTokens:  out.UsageMetadata.PromptTokenCount,
		OutputTokens: out.UsageMetadata.CandidatesTokenCount,
	}, nil
}

func textFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "" || p.Type == "text" || p.Type == "output_text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}
