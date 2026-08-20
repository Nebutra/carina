// Package modelrouter is the unified model call interface: provider registry
// with ordered fallback, per-provider token accounting, and optional streaming.
package modelrouter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type Request struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Prompt          string `json:"prompt"`
	StablePrefix    string `json:"stable_prefix,omitempty"`
	// StableSections, when set, are the cacheable prefix split into
	// constitution / workspace / catalog(+task) text blocks. Adapters that
	// do not understand sections keep using StablePrefix as one blob.
	StableSections []string `json:"stable_sections,omitempty"`
	VolatileSuffix string   `json:"volatile_suffix,omitempty"`
	// Media carries image parts for vision-capable models. Raw bytes here,
	// provider-specific encoding (base64 data URI vs source block) in each
	// adapter. Callers are responsible for gating on the model's declared
	// input modalities BEFORE attaching media; adapters that predate media
	// support simply ignore the field, so a text-only path degrades to
	// whatever textual placeholder the caller already put in the prompt.
	Media []MediaPart `json:"media,omitempty"`
	// Tools is the optional native function catalog. Adapters that predate
	// tool calling ignore it; the agent loop then stays on JSON ReAct.
	Tools []ToolSpec `json:"tools,omitempty"`
}

// MediaPart is one image attached to a request. MediaType is a sniffed,
// allowlisted MIME type (image/png, image/jpeg, image/gif, image/webp);
// Data is the raw bytes, never pre-encoded.
type MediaPart struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

type Response struct {
	Provider                 string     `json:"provider"`
	Model                    string     `json:"model"`
	Text                     string     `json:"text"`
	InputTokens              int        `json:"input_tokens"`
	OutputTokens             int        `json:"output_tokens"`
	CacheReadTokens          int        `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens         int        `json:"cache_write_tokens,omitempty"`
	EffectiveReasoningEffort string     `json:"effective_reasoning_effort,omitempty"`
	ToolCalls                []ToolCall `json:"tool_calls,omitempty"`
}

// Provider is implemented by model backends (Anthropic, OpenAI, local, plugin).
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

// StreamEvent carries private provider output. Reset invalidates every delta
// previously emitted for the current attempt before a provider or protocol
// fallback starts.
type StreamEvent struct {
	Delta string
	Reset bool
}

type StreamCallback func(StreamEvent)

// StreamingProvider is an optional provider capability. Providers that do not
// implement it retain the Complete contract; Router.Stream explicitly adapts
// their final response into one delta.
type StreamingProvider interface {
	Provider
	Stream(ctx context.Context, req Request, callback StreamCallback) (*Response, error)
}

type Usage struct {
	Requests         int `json:"requests"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

type Router struct {
	mu         sync.RWMutex
	providers  []Provider
	embeddings []EmbeddingsProvider
	rerank     []RerankProvider
	usage      map[string]*Usage
}

func New() *Router {
	return &Router{usage: make(map[string]*Usage)}
}

func (r *Router) RegisterProvider(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, p)
}

// ProviderNames returns completion provider identities without exposing
// implementations or credentials.
func (r *Router) ProviderNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Name())
	}
	return out
}

// Complete tries providers in registration order until one succeeds.
func (r *Router) Complete(ctx context.Context, req Request) (*Response, error) {
	return r.complete(ctx, req, nil)
}

// Stream tries providers in registration order while preserving the Complete
// fallback for non-streaming providers. Every failed provider resets its
// private delta generation, including the final provider in the chain.
func (r *Router) Stream(ctx context.Context, req Request, callback StreamCallback) (*Response, error) {
	return r.complete(ctx, req, callback)
}

func (r *Router) complete(ctx context.Context, req Request, callback StreamCallback) (*Response, error) {
	r.mu.RLock()
	providers := append([]Provider(nil), r.providers...)
	r.mu.RUnlock()

	if len(providers) == 0 {
		return nil, errors.New("modelrouter: no providers registered")
	}
	if target, model, ok := targetedModel(req.Model, providers); ok {
		providers = []Provider{target}
		req.Model = model
	}

	var errs []error
	for _, p := range providers {
		var resp *Response
		var err error
		if streaming, ok := p.(StreamingProvider); ok && callback != nil {
			resp, err = streaming.Stream(ctx, req, callback)
		} else {
			resp, err = p.Complete(ctx, req)
			if err == nil && callback != nil && resp != nil && resp.Text != "" {
				callback(StreamEvent{Delta: resp.Text})
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			if callback != nil {
				// A failed provider may already have emitted private deltas. Reset
				// immediately, including after the final provider, so callers never
				// retain output from a failed generation.
				callback(StreamEvent{Reset: true})
			}
			continue
		}
		r.recordUsage(p.Name(), resp)
		return resp, nil
	}
	return nil, fmt.Errorf("modelrouter: all providers failed: %w", errors.Join(errs...))
}

func targetedModel(model string, providers []Provider) (Provider, string, bool) {
	prefix, suffix, ok := strings.Cut(model, "/")
	if !ok || prefix == "" {
		return nil, "", false
	}
	for _, p := range providers {
		if p.Name() == prefix {
			if suffix == "" {
				suffix = "default"
			}
			return p, suffix, true
		}
	}
	return nil, "", false
}

func (r *Router) UsageByProvider() map[string]Usage {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Usage, len(r.usage))
	for name, u := range r.usage {
		out[name] = *u
	}
	return out
}

func (r *Router) recordUsage(name string, resp *Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.usage[name]
	if !ok {
		u = &Usage{}
		r.usage[name] = u
	}
	u.Requests++
	u.InputTokens += resp.InputTokens
	u.OutputTokens += resp.OutputTokens
	u.CacheReadTokens += resp.CacheReadTokens
	u.CacheWriteTokens += resp.CacheWriteTokens
}
