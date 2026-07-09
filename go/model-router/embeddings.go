// Embeddings routing (docs/plans/code-intelligence.md, V2): the
// EmbeddingsProvider registry mirrors the Provider/Complete idiom —
// registration-order fallback, "provider/model" targeting, and usage
// accounting (input tokens only). Deliberate delta from chat providers:
// no mock embeddings provider is registered by default, so
// HasEmbeddingsProvider is a truthful degrade signal (false = the semantic
// layer is off and every consumer silently stays keyword-only).
package modelrouter

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// EmbeddingsRequest asks for one vector per input string.
type EmbeddingsRequest struct {
	Model  string   `json:"model"` // "" / "default", or "provider/model" targeting
	Inputs []string `json:"inputs"`
}

// EmbeddingsResponse carries one vector per input, all with the same dims.
type EmbeddingsResponse struct {
	Provider    string      `json:"provider"`
	Model       string      `json:"model"` // resolved model — used in the index model_id
	Vectors     [][]float32 `json:"vectors"`
	InputTokens int         `json:"input_tokens"`
}

// EmbeddingsProvider is implemented by embedding backends (BYOK).
type EmbeddingsProvider interface {
	Name() string
	Embed(ctx context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error)
}

// RegisterEmbeddingsProvider adds an embeddings backend to the fallback order.
func (r *Router) RegisterEmbeddingsProvider(p EmbeddingsProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.embeddings = append(r.embeddings, p)
}

// Embed tries embeddings providers in registration order until one succeeds.
func (r *Router) Embed(ctx context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error) {
	r.mu.RLock()
	providers := append([]EmbeddingsProvider(nil), r.embeddings...)
	r.mu.RUnlock()

	if len(providers) == 0 {
		return nil, errors.New("modelrouter: no embeddings providers registered")
	}
	if target, model, ok := targetedEmbeddingsModel(req.Model, providers); ok {
		providers = []EmbeddingsProvider{target}
		req.Model = model
	}

	var errs []error
	for _, p := range providers {
		resp, err := p.Embed(ctx, req)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			continue
		}
		r.recordEmbedUsage(p.Name(), resp)
		return resp, nil
	}
	return nil, fmt.Errorf("modelrouter: all embeddings providers failed: %w", errors.Join(errs...))
}

func targetedEmbeddingsModel(model string, providers []EmbeddingsProvider) (EmbeddingsProvider, string, bool) {
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

func (r *Router) recordEmbedUsage(name string, resp *EmbeddingsResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.usage[name]
	if !ok {
		u = &Usage{}
		r.usage[name] = u
	}
	u.Requests++
	u.InputTokens += resp.InputTokens
}

// HasEmbeddingsProvider is the degrade-path signal: false means the semantic
// layer is disabled and consumers keep V1 keyword-only behavior.
func (r *Router) HasEmbeddingsProvider() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.embeddings) > 0
}

// HasEmbeddingsProviderNamed reports whether an embeddings provider is
// registered under exactly this name — callers use it to refuse "provider/
// model" targeting that would otherwise fall back to a different provider.
func (r *Router) HasEmbeddingsProviderNamed(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.embeddings {
		if p.Name() == name {
			return true
		}
	}
	return false
}
