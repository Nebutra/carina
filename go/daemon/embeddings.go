package daemon

// V2 semantic layer (docs/plans/code-intelligence.md): the BYOK embeddings
// adapter + registration table, the best-effort post-index embedding sync,
// and the LSP workspace-scoping helpers for code.def / code.refs. The Rust
// kernel performs zero network I/O — only this daemon talks to embedding
// providers, through the model-router provider idiom (auth.Chain credentials
// + the shared providerBase HTTP adapter). No configured provider means the
// semantic layer is silently off and every code.* tool keeps V1 behavior.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/lsp"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

// maxEmbedChars caps one chunk's text sent to an embeddings provider.
const maxEmbedChars = 8192

// embedTimeout bounds one embeddings provider call — the query-time embed
// and every sync batch alike (the rerankTimeout convention, well under
// providerHTTPTimeout): a hanging endpoint degrades the semantic channel in
// seconds instead of stalling each code.search for the full HTTP timeout. A
// var so tests can shrink the deadline.
var embedTimeout = 10 * time.Second

// maxSyncBatches bounds one syncEmbeddings pass (batches of pending chunks).
const maxSyncBatches = 200

// embedBatchSize is how many pending chunks one provider call embeds (also
// well under the kernel's 256-per-call embed_store cap).
const embedBatchSize = 64

// embeddingsBackend is one BYOK registration table entry. encodingFormat is
// the request's encoding_format value; "" omits the field entirely — the
// Voyage API accepts only null or "base64" there and 400s on OpenAI's "float".
type embeddingsBackend struct {
	id             string
	baseURL        string
	defaultModel   string
	envKey         string
	encodingFormat string
}

// embeddingsBackends is the BYOK registration table: OpenAI-compatible
// /embeddings endpoints, in fallback priority order.
var embeddingsBackends = []embeddingsBackend{
	{"openai", "https://api.openai.com/v1", "text-embedding-3-small", "OPENAI_API_KEY", "float"},
	{"voyage", "https://api.voyageai.com/v1", "voyage-code-3", "VOYAGE_API_KEY", ""},
}

// registerEmbeddingsProviders registers the BYOK embeddings backends
// (OpenAI-compatible /embeddings: OpenAI + Voyage). Deliberate delta from
// chat providers: a backend registers ONLY when its auth.Chain resolves a
// credential, so degrade happens before any network attempt. Offline mode
// registers nothing. CARINA_EMBEDDINGS_MODEL ("provider/model") overrides
// targeting. Returns the default index model id ("provider/model" of the
// first registered backend, "" when none).
func registerEmbeddingsProviders(router *modelrouter.Router, offline bool, store *auth.Store) string {
	if offline {
		return ""
	}
	defaultID := ""
	for _, b := range embeddingsBackends {
		chain := auth.ProviderChain(b.id, []string{b.envKey}, store, nil)
		if cred, ok := chain.Resolve(); !ok || strings.TrimSpace(cred.Value) == "" {
			continue
		}
		router.RegisterEmbeddingsProvider(&openAIEmbeddingsProvider{providerBase: providerBase{
			id: b.id, baseURL: b.baseURL, defaultModel: b.defaultModel, auth: chain,
		}, encodingFormat: b.encodingFormat})
		if defaultID == "" {
			defaultID = b.id + "/" + b.defaultModel
		}
	}
	return defaultID
}

// openAIEmbeddingsProvider is the OpenAI-compatible /embeddings adapter
// (covers OpenAI text-embedding-3-* and Voyage voyage-code-3).
type openAIEmbeddingsProvider struct {
	providerBase
	encodingFormat string // "" omits encoding_format (Voyage: null/"base64" only)
}

func (p *openAIEmbeddingsProvider) Name() string { return p.id }

func (p *openAIEmbeddingsProvider) Embed(ctx context.Context, req modelrouter.EmbeddingsRequest) (*modelrouter.EmbeddingsResponse, error) {
	cred, hasCred, err := p.credential()
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(req.Model)
	if model == "" || model == "default" {
		model = p.defaultModel
	}
	payload := map[string]any{
		"model": model,
		"input": req.Inputs,
	}
	if p.encodingFormat != "" {
		payload["encoding_format"] = p.encodingFormat
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("embeddings"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	if hasCred {
		httpReq.Header.Set("Authorization", "Bearer "+cred.Value)
	}
	p.applyExtraHeaders(httpReq.Header)
	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", p.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError(p.id, resp)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"` // Voyage reports only this
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", p.id, err)
	}
	if len(out.Data) != len(req.Inputs) {
		return nil, fmt.Errorf("%s: %d embeddings for %d inputs", p.id, len(out.Data), len(req.Inputs))
	}
	vectors := make([][]float32, len(req.Inputs))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vectors) || len(d.Embedding) == 0 {
			return nil, fmt.Errorf("%s: malformed embedding at index %d", p.id, d.Index)
		}
		vectors[d.Index] = d.Embedding
	}
	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("%s: missing embedding for input %d", p.id, i)
		}
	}
	responseModel := strings.TrimSpace(out.Model)
	if responseModel == "" {
		responseModel = model
	}
	inputTokens := out.Usage.PromptTokens
	if inputTokens == 0 {
		inputTokens = out.Usage.TotalTokens
	}
	return &modelrouter.EmbeddingsResponse{
		Provider:    p.id,
		Model:       responseModel,
		Vectors:     vectors,
		InputTokens: inputTokens,
	}, nil
}

// embeddingsModelID is the index model key, "<provider>/<resolved model>" —
// changing either re-embeds under a fresh key. Empty when the semantic layer
// is off (no registered embeddings provider).
func (d *Daemon) embeddingsModelID() string {
	if !d.router.HasEmbeddingsProvider() {
		return ""
	}
	if v := strings.TrimSpace(os.Getenv("CARINA_EMBEDDINGS_MODEL")); v != "" {
		// A "provider/model" override must name a provider that actually
		// registered: router fallback must never send workspace chunks to a
		// provider the user did not select. A known backend missing its key
		// and an unknown provider prefix ("cohere/…") both switch the
		// semantic layer off.
		if prefix, _, ok := strings.Cut(v, "/"); ok && !d.router.HasEmbeddingsProviderNamed(prefix) {
			return ""
		}
		return v
	}
	if d.embedModelDefault != "" {
		return d.embedModelDefault
	}
	return "default"
}

// syncFailure classifies a syncEmbeddings error for the observable-degrade
// surfaces (V3 §B): the reason (never content) goes to the audit chain and
// daemon.status; Error() stays the underlying text for the log line.
type syncFailure struct {
	reason string // provider-error | dims-mismatch | kernel-error
	err    error
}

func (f *syncFailure) Error() string { return f.err.Error() }
func (f *syncFailure) Unwrap() error { return f.err }

// syncFailureReason extracts the classified reason ("provider-error" for
// unclassified errors — the conservative default).
func syncFailureReason(err error) string {
	var f *syncFailure
	if errors.As(err, &f) {
		return f.reason
	}
	return "provider-error"
}

// syncEmbeddings pushes pending chunk embeddings into the kernel index,
// best-effort (an error never fails the calling tool; callers surface it via
// noteEmbeddingSyncFailure): pending_chunks -> truncate to maxEmbedChars ->
// router.Embed -> embed_store, until the backlog drains, an error occurs, or
// maxSyncBatches is hit. With no embeddings provider it returns nil
// immediately (the tested degrade path).
func (d *Daemon) syncEmbeddings(sessionID string) error {
	modelID := d.embeddingsModelID()
	if modelID == "" {
		return nil
	}
	for batch := 0; batch < maxSyncBatches; batch++ {
		pending, err := d.kern.IndexPendingChunks(sessionID, modelID, embedBatchSize)
		if err != nil {
			return &syncFailure{reason: "kernel-error", err: err}
		}
		if len(pending.Chunks) == 0 {
			// The backlog is drained: record the healthy pass so a previous
			// failure heals observably (and searchIndexDegraded stops
			// retrying) even when this pass had nothing left to store.
			d.noteEmbedSyncHealthy(sessionID, modelID)
			return nil
		}
		inputs := make([]string, len(pending.Chunks))
		for i, c := range pending.Chunks {
			inputs[i] = truncateEmbedText(c.Content)
		}
		resp, err := d.embedWithDeadline(modelrouter.EmbeddingsRequest{Model: modelID, Inputs: inputs})
		if err != nil {
			return &syncFailure{reason: "provider-error", err: err}
		}
		if len(resp.Vectors) != len(pending.Chunks) {
			return &syncFailure{reason: "provider-error",
				err: fmt.Errorf("embeddings: %d vectors for %d chunks", len(resp.Vectors), len(pending.Chunks))}
		}
		dims := len(resp.Vectors[0])
		items := make([]kernel.ChunkEmbedding, len(pending.Chunks))
		for i, c := range pending.Chunks {
			if len(resp.Vectors[i]) != dims || dims == 0 {
				return &syncFailure{reason: "dims-mismatch",
					err: fmt.Errorf("embeddings: inconsistent dims (%d vs %d)", len(resp.Vectors[i]), dims)}
			}
			items[i] = kernel.ChunkEmbedding{ChunkID: c.ChunkID, ContentHash: c.ContentHash, Vector: resp.Vectors[i]}
		}
		res, err := d.kern.IndexEmbedStore(sessionID, modelID, dims, items)
		if err != nil {
			return &syncFailure{reason: "kernel-error", err: err}
		}
		d.noteEmbedStoreSuccess(sessionID, modelID, dims)
		if res.TotalPending == 0 {
			return nil
		}
	}
	return nil
}

// embedWithDeadline is the one router.Embed chokepoint: every embeddings
// provider call — query-time or sync batch — runs under embedTimeout (the
// rerankHits deadline treatment), so a hanging endpoint degrades observably
// instead of stalling the calling tool for the full providerHTTPTimeout.
func (d *Daemon) embedWithDeadline(req modelrouter.EmbeddingsRequest) (*modelrouter.EmbeddingsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), embedTimeout)
	defer cancel()
	return d.router.Embed(ctx, req)
}

// truncateEmbedText caps chunk text sent to a provider at maxEmbedChars.
func truncateEmbedText(text string) string {
	if len(text) > maxEmbedChars {
		return text[:maxEmbedChars]
	}
	return text
}

// searchIndexDegraded is code.search's kernel query: with the semantic layer
// on it embeds the query first and runs the three-way (exact+BM25+cosine)
// search; on embed failure — or with no provider — it falls back cleanly to
// the V1 keyword-only search. The second return is the semantic-channel
// degrade reason ("" = semantic on): no-provider, provider-error,
// kernel-error, or dims-mismatch — the V3 observable-degrade header/audit
// input. "semantic:on" is kernel-truth, not just "the query embed worked":
// a session whose last sync failed (or that has no sync record, e.g. a fresh
// process) re-syncs first — healing a recovered provider instead of staying
// degraded until the next write — and the kernel's vector_channel counters
// catch a store the cosine channel could not actually rank (empty, or dims
// changed across a restart).
func (d *Daemon) searchIndexDegraded(sessionID, query string, limit int) (json.RawMessage, string, error) {
	modelID := d.embeddingsModelID()
	if modelID == "" {
		raw, err := d.kern.IndexSearch(sessionID, query, limit)
		return raw, "no-provider", err
	}
	if !d.embedSyncHealthy(sessionID) {
		if err := d.syncEmbeddings(sessionID); err != nil {
			// The backlog is (still) unembedded: claiming semantic:on over a
			// store known to be missing vectors would be a lie. Surface the
			// retry failure like every sync failure and degrade.
			d.noteEmbeddingSyncFailure(sessionID, "", err)
			raw, kerr := d.kern.IndexSearch(sessionID, query, limit)
			return raw, syncFailureReason(err), kerr
		}
	}
	resp, err := d.embedWithDeadline(modelrouter.EmbeddingsRequest{
		Model: modelID, Inputs: []string{truncateEmbedText(query)},
	})
	if err != nil || len(resp.Vectors) != 1 || len(resp.Vectors[0]) == 0 {
		raw, err := d.kern.IndexSearch(sessionID, query, limit)
		return raw, "provider-error", err
	}
	if dims := d.lastEmbedDims(sessionID); dims > 0 && len(resp.Vectors[0]) != dims {
		// A wrong-dims query vector would silently rank as noise against the
		// stored vectors — degrade to keyword-only instead (cheap in-process
		// short-circuit; the kernel counters below catch the same state when
		// this process has no dims memory).
		raw, err := d.kern.IndexSearch(sessionID, query, limit)
		return raw, "dims-mismatch", err
	}
	raw, err := d.kern.IndexSearchVector(sessionID, query, limit, modelID, resp.Vectors[0])
	if err != nil {
		return raw, "", err
	}
	// Kernel-truth channel check: stored vectors the cosine channel skipped
	// wholesale (dims differ from the query) mean the semantic channel
	// contributed nothing — the result is identical to keyword-only, so keep
	// it, but say so.
	return raw, vectorChannelDegrade(raw), nil
}

// vectorChannelDegrade reads the kernel's vector_channel liveness counters
// out of a search result: vectors stored for the model that the query could
// not rank a single one of (stored > 0, live == 0) is the observable
// dims-mismatch state. An absent field (keyword-only result) or an empty
// store (nothing to embed — a failed sync never reaches this point) is not a
// degrade.
func vectorChannelDegrade(raw json.RawMessage) string {
	var res struct {
		VectorChannel *struct {
			Stored int `json:"stored"`
			Live   int `json:"live"`
		} `json:"vector_channel"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.VectorChannel == nil {
		return ""
	}
	if res.VectorChannel.Stored > 0 && res.VectorChannel.Live == 0 {
		return "dims-mismatch"
	}
	return ""
}

// searchIndex is the header-less searchIndexDegraded wrapper (kept for
// callers that need the raw result without the degrade state).
func (d *Daemon) searchIndex(sessionID, query string, limit int) (json.RawMessage, error) {
	raw, _, err := d.searchIndexDegraded(sessionID, query, limit)
	return raw, err
}

// lspServerForExt is the code.def/code.refs server lookup seam; tests
// override it to simulate absent or fake language servers.
var lspServerForExt = serverForExt

// filterWorkspaceLocations drops every LSP location outside root: language
// servers may answer with stdlib/global paths, and LSP reads are file reads —
// nothing outside the session workspace may be rendered to the agent. Both
// sides are symlink-canonicalized before the prefix compare (macOS /tmp vs
// /private/tmp), so a canonical server answer under a symlinked root — or
// vice versa — is kept, while out-of-workspace paths still drop.
func filterWorkspaceLocations(root string, locs []lsp.Location) []lsp.Location {
	canonRoot := canonicalPath(root)
	var kept []lsp.Location
	for _, loc := range locs {
		p := canonicalPath(loc.Path)
		if strings.HasPrefix(p, canonRoot+string(filepath.Separator)) {
			loc.Path = p
			kept = append(kept, loc)
		}
	}
	return kept
}

// canonicalPath resolves symlinks for containment comparisons. When the leaf
// does not exist (a location a server predicts, a deleted file) the parent
// directory canonicalizes instead; a fully unresolvable path stays cleaned —
// it can only ever fail a containment check, never widen one.
func canonicalPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	if r, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(r, base)
	}
	return p
}
