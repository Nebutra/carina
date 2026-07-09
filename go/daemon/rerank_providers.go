package daemon

// V4 §C (docs/plans/code-intelligence.md): real rerank providers behind the
// V3 reranker seam. BYOK registration table next to the embeddings one — a
// backend registers ONLY when its auth.Chain resolves a credential, offline
// registers nothing. Selection is explicit and never falls back:
// CARINA_RERANK_MODEL ("provider/model") names the one provider that may see
// query + candidate snippets (content egress); an unregistered prefix keeps
// the stage off with rerank:off(no-provider) + a code_rerank_degraded audit
// event. The rerank stage runs under rerankTimeout; timeout/error/invalid
// permutation fall back to the un-reranked kernel order.

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

// rerankTimeout bounds one rerank provider call (the lspQueryTimeout
// convention, well under providerHTTPTimeout). A var so tests can shrink the
// deadline.
var rerankTimeout = 10 * time.Second

// rerankBackend is one BYOK rerank registration table entry.
type rerankBackend struct {
	id           string
	baseURL      string
	defaultModel string
	envKey       string
}

// rerankBackends is the BYOK rerank registration table.
var rerankBackends = []rerankBackend{
	{"voyage", "https://api.voyageai.com/v1", "rerank-2", "VOYAGE_API_KEY"},
	{"cohere", "https://api.cohere.com/v2", "rerank-english-v3.0", "COHERE_API_KEY"},
}

// registerRerankProviders registers the BYOK rerank backends (voyage +
// cohere) — only when a credential resolves, never in offline mode (the
// registerEmbeddingsProviders delta, verbatim).
func registerRerankProviders(router *modelrouter.Router, offline bool, store *auth.Store) {
	if offline {
		return
	}
	for _, b := range rerankBackends {
		chain := auth.ProviderChain(b.id, []string{b.envKey}, store, nil)
		if cred, ok := chain.Resolve(); !ok || strings.TrimSpace(cred.Value) == "" {
			continue
		}
		base := providerBase{id: b.id, baseURL: b.baseURL, defaultModel: b.defaultModel, auth: chain}
		switch b.id {
		case "voyage":
			router.RegisterRerankProvider(&voyageRerankProvider{providerBase: base})
		case "cohere":
			router.RegisterRerankProvider(&cohereRerankProvider{providerBase: base})
		}
	}
}

// rerankModelSelection resolves the V4 explicit rerank selection: with
// CARINA_RERANK_MODEL unset the V3 seam (configuredReranker) decides, exactly
// as before; a set value must be "provider/model" naming a REGISTERED rerank
// provider — anything else (bare model, unknown provider, a known backend
// missing its key, offline) keeps the stage off with the "no-provider"
// degrade reason. Snippets are content egress: they go only to the provider
// the user explicitly selected, never to a fallback.
func (d *Daemon) rerankModelSelection() (Reranker, string) {
	v := strings.TrimSpace(os.Getenv("CARINA_RERANK_MODEL"))
	if v == "" {
		return configuredReranker(), ""
	}
	prefix, _, ok := strings.Cut(v, "/")
	if !ok || prefix == "" || !d.router.HasRerankProviderNamed(prefix) {
		return nil, "no-provider"
	}
	return &routerReranker{router: d.router, name: prefix, model: v}, ""
}

// routerReranker adapts the model-router rerank path to the V3 Reranker
// seam: candidates' snippets become the provider documents, the descending-
// relevance results become the permutation (validPermutation downstream
// rejects short/duplicate answers).
type routerReranker struct {
	router *modelrouter.Router
	name   string
	model  string
}

func (r *routerReranker) Name() string { return r.name }

func (r *routerReranker) Rerank(ctx context.Context, query string, cands []rerankCandidate) ([]int, error) {
	docs := make([]string, len(cands))
	for i, c := range cands {
		docs[i] = c.Snippet
	}
	resp, err := r.router.Rerank(ctx, modelrouter.RerankRequest{
		Model: r.model, Query: query, Documents: docs, TopK: len(docs),
	})
	if err != nil {
		return nil, err
	}
	// The design permutation (V4 §C): returned indices in provider order, then
	// any unreturned indices in kernel order — a provider answering with fewer
	// (index, score) pairs than documents (dropped/deduped/over-limit) is a
	// successful partial ordering, not an error. Malformed answers (duplicate
	// or out-of-range indices) still fail validPermutation downstream.
	perm := make([]int, 0, len(cands))
	seen := make([]bool, len(cands))
	for _, res := range resp.Results {
		perm = append(perm, res.Index)
		if res.Index >= 0 && res.Index < len(seen) {
			seen[res.Index] = true
		}
	}
	for i, returned := range seen {
		if !returned {
			perm = append(perm, i)
		}
	}
	return perm, nil
}

// rerankWireResult is the shared (index, relevance_score) response row.
type rerankWireResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// voyageRerankProvider adapts POST /v1/rerank (rerank-2 wire shape:
// {model, query, documents, top_k, return_documents:false} ->
// {data: [{index, relevance_score}], model, usage.total_tokens}).
type voyageRerankProvider struct {
	providerBase
}

func (p *voyageRerankProvider) Name() string { return p.id }

func (p *voyageRerankProvider) Rerank(ctx context.Context, req modelrouter.RerankRequest) (*modelrouter.RerankResponse, error) {
	model := rerankModelOrDefault(req.Model, p.defaultModel)
	payload := struct {
		Model           string   `json:"model"`
		Query           string   `json:"query"`
		Documents       []string `json:"documents"`
		TopK            int      `json:"top_k"`
		ReturnDocuments bool     `json:"return_documents"`
	}{Model: model, Query: req.Query, Documents: req.Documents, TopK: req.TopK}
	var out struct {
		Data  []rerankWireResult `json:"data"`
		Model string             `json:"model"`
	}
	if err := p.postRerank(ctx, payload, &out); err != nil {
		return nil, err
	}
	responseModel := strings.TrimSpace(out.Model)
	if responseModel == "" {
		responseModel = model
	}
	return &modelrouter.RerankResponse{
		Provider: p.id, Model: responseModel, Results: passThroughResults(out.Data),
	}, nil
}

// cohereRerankProvider adapts POST /v2/rerank (v3 wire shape:
// {model, query, documents, top_n} ->
// {results: [{index, relevance_score}], meta.billed_units.search_units}).
type cohereRerankProvider struct {
	providerBase
}

func (p *cohereRerankProvider) Name() string { return p.id }

func (p *cohereRerankProvider) Rerank(ctx context.Context, req modelrouter.RerankRequest) (*modelrouter.RerankResponse, error) {
	model := rerankModelOrDefault(req.Model, p.defaultModel)
	payload := struct {
		Model     string   `json:"model"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
		TopN      int      `json:"top_n"`
	}{Model: model, Query: req.Query, Documents: req.Documents, TopN: req.TopK}
	var out struct {
		Results []rerankWireResult `json:"results"`
	}
	if err := p.postRerank(ctx, payload, &out); err != nil {
		return nil, err
	}
	return &modelrouter.RerankResponse{
		Provider: p.id, Model: model, Results: passThroughResults(out.Results),
	}, nil
}

// postRerank is the shared Bearer-authenticated POST <baseURL>/rerank call
// (the openAIEmbeddingsProvider HTTP idiom: providerBase credential, headers,
// and governed client).
func (p *providerBase) postRerank(ctx context.Context, payload, out any) error {
	cred, hasCred, err := p.credential()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("rerank"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("content-type", "application/json")
	if hasCred {
		httpReq.Header.Set("Authorization", "Bearer "+cred.Value)
	}
	p.applyExtraHeaders(httpReq.Header)
	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return fmt.Errorf("%s: request: %w", p.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(p.id, resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode: %w", p.id, err)
	}
	return nil
}

// rerankModelOrDefault resolves ""/"default" to the backend default model.
func rerankModelOrDefault(model, def string) string {
	model = strings.TrimSpace(model)
	if model == "" || model == "default" {
		return def
	}
	return model
}

// passThroughResults converts wire rows in provider order (never re-sorted:
// providers answer in descending relevance and the daemon must not second-
// guess the ordering it asked for).
func passThroughResults(rows []rerankWireResult) []modelrouter.RerankResult {
	results := make([]modelrouter.RerankResult, len(rows))
	for i, r := range rows {
		results[i] = modelrouter.RerankResult{Index: r.Index, Score: r.RelevanceScore}
	}
	return results
}
