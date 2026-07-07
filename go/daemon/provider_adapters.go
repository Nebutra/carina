package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

const providerHTTPTimeout = 120 * time.Second

type providerBase struct {
	id           string
	baseURL      string
	defaultModel string
	auth         *auth.Chain
	client       *http.Client
	noAuth       bool
	headers      map[string]string
}

func (p *providerBase) name() string { return p.id }

func (p *providerBase) model(req modelrouter.Request) string {
	model := strings.TrimSpace(req.Model)
	if model == "" || model == "default" {
		model = p.defaultModel
	}
	return model
}

func (p *providerBase) credential() (auth.Credential, bool, error) {
	if p.auth == nil {
		if p.noAuth {
			return auth.Credential{}, false, nil
		}
		return auth.Credential{}, false, fmt.Errorf("%s: credential not set", p.id)
	}
	cred, ok := p.auth.Resolve()
	if !ok || strings.TrimSpace(cred.Value) == "" {
		if p.noAuth {
			return auth.Credential{}, false, nil
		}
		return auth.Credential{}, false, fmt.Errorf("%s: credential not set", p.id)
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
	model := o.model(req)
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 2048,
		"messages":   []map[string]string{{"role": "user", "content": req.Prompt}},
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint("chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	if hasCred {
		httpReq.Header.Set("Authorization", "Bearer "+cred.Value)
	}
	o.applyExtraHeaders(httpReq.Header)
	resp, err := o.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", o.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s: status %d", o.id, resp.StatusCode)
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
	return &modelrouter.Response{
		Provider:     o.Name(),
		Model:        model,
		Text:         text,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
	}, nil
}

func (o *openAIProvider) completeResponses(ctx context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	cred, hasCred, err := o.credential()
	if err != nil {
		return nil, err
	}
	model := o.model(req)
	body, _ := json.Marshal(map[string]any{
		"model":             model,
		"input":             req.Prompt,
		"max_output_tokens": 2048,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint("responses"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	if hasCred {
		httpReq.Header.Set("Authorization", "Bearer "+cred.Value)
	}
	o.applyExtraHeaders(httpReq.Header)
	resp, err := o.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", o.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s: status %d", o.id, resp.StatusCode)
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
	return &modelrouter.Response{
		Provider:     o.Name(),
		Model:        model,
		Text:         text,
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}, nil
}

type geminiProvider struct{ providerBase }

func (g *geminiProvider) Name() string { return g.name() }

func (g *geminiProvider) Complete(ctx context.Context, req modelrouter.Request) (*modelrouter.Response, error) {
	cred, hasCred, err := g.credential()
	if err != nil {
		return nil, err
	}
	model := g.model(req)
	endpoint := g.endpoint("models/" + url.PathEscape(model) + ":generateContent")
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"contents": []map[string]any{{
			"parts": []map[string]string{{"text": req.Prompt}},
		}},
		"generationConfig": map[string]any{"maxOutputTokens": 2048},
	})
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
	resp, err := g.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", g.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s: status %d", g.id, resp.StatusCode)
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
		Model:        model,
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
