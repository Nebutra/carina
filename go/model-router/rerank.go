// Rerank routing (docs/plans/code-intelligence.md, V4 §C): the
// RerankProvider registry mirrors the EmbeddingsProvider idiom —
// registration-order fallback, "provider/model" targeting, and usage
// accounting (requests only). No default/mock rerank provider is registered,
// so HasRerankProviderNamed is a truthful no-fallback guard: the daemon
// sends query + candidate snippets (content egress) only to a provider the
// user explicitly selected.
package modelrouter

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// RerankRequest asks a provider to order Documents by relevance to Query.
type RerankRequest struct {
	Model     string   `json:"model"` // "" / "default", or "provider/model" targeting
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopK      int      `json:"top_k"` // callers pass len(Documents): full ordering
}

// RerankResult is one (candidate index, relevance score) pair.
type RerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// RerankResponse carries results in descending relevance (provider order).
type RerankResponse struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Results  []RerankResult `json:"results"`
}

// RerankProvider is implemented by rerank backends (BYOK).
type RerankProvider interface {
	Name() string
	Rerank(ctx context.Context, req RerankRequest) (*RerankResponse, error)
}

// RegisterRerankProvider adds a rerank backend to the fallback order.
func (r *Router) RegisterRerankProvider(p RerankProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rerank = append(r.rerank, p)
}

// Rerank tries rerank providers in registration order until one succeeds
// (same fallback + "provider/model" targeting semantics as Embed) and
// records usage under the provider name.
func (r *Router) Rerank(ctx context.Context, req RerankRequest) (*RerankResponse, error) {
	r.mu.RLock()
	providers := append([]RerankProvider(nil), r.rerank...)
	r.mu.RUnlock()

	if len(providers) == 0 {
		return nil, errors.New("modelrouter: no rerank providers registered")
	}
	if target, model, ok := targetedRerankModel(req.Model, providers); ok {
		providers = []RerankProvider{target}
		req.Model = model
	}

	var errs []error
	for _, p := range providers {
		resp, err := p.Rerank(ctx, req)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			continue
		}
		r.recordRerankUsage(p.Name())
		return resp, nil
	}
	return nil, fmt.Errorf("modelrouter: all rerank providers failed: %w", errors.Join(errs...))
}

func targetedRerankModel(model string, providers []RerankProvider) (RerankProvider, string, bool) {
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

func (r *Router) recordRerankUsage(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.usage[name]
	if !ok {
		u = &Usage{}
		r.usage[name] = u
	}
	u.Requests++
}

// HasRerankProvider reports whether any rerank backend is registered.
func (r *Router) HasRerankProvider() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.rerank) > 0
}

// HasRerankProviderNamed reports whether a rerank provider is registered
// under exactly this name — the no-fallback guard hook: callers refuse
// "provider/model" targeting that would otherwise route snippets to a
// provider the user did not select.
func (r *Router) HasRerankProviderNamed(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.rerank {
		if p.Name() == name {
			return true
		}
	}
	return false
}
