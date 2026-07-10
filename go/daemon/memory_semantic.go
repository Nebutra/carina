package daemon

import (
	"fmt"
	"math"
	"sort"
	"strings"

	modelrouter "github.com/Nebutra/carina/go/model-router"
)

const (
	memorySearchModeLexical  = "lexical"
	memorySearchModeSemantic = "semantic"
	memorySearchModeAuto     = "auto"
)

func normalizeMemorySearchMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return memorySearchModeLexical, nil
	}
	switch mode {
	case memorySearchModeLexical, memorySearchModeSemantic, memorySearchModeAuto:
		return mode, nil
	default:
		return "", fmt.Errorf("memory search mode must be lexical, semantic, or auto")
	}
}

func (d *Daemon) searchMemory(scope memoryScope, query, target string, limit int, mode, model string) (memorySearchResult, error) {
	mode, err := normalizeMemorySearchMode(mode)
	if err != nil {
		return memorySearchResult{}, err
	}
	if mode == memorySearchModeLexical {
		return d.memory.search(scope, query, target, limit)
	}
	result, err := d.semanticMemorySearch(scope, query, target, limit, model)
	if err == nil || mode == memorySearchModeSemantic {
		return result, err
	}
	fallback, ferr := d.memory.search(scope, query, target, limit)
	if ferr != nil {
		return memorySearchResult{}, ferr
	}
	fallback.Semantic = &memorySearchSemanticStatus{Enabled: false, Reason: semanticMemoryFallbackReason(err)}
	return fallback, nil
}

func (d *Daemon) semanticMemorySearch(scope memoryScope, query, target string, limit int, model string) (memorySearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return memorySearchResult{}, fmt.Errorf("query is required")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	modelID, err := d.memoryEmbeddingsModelID(model)
	if err != nil {
		return memorySearchResult{}, err
	}
	candidates, err := d.memory.searchCandidates(scope, target)
	if err != nil {
		return memorySearchResult{}, err
	}
	result := memorySearchResult{
		Scope:    scope,
		Query:    query,
		Mode:     memorySearchModeSemantic,
		Semantic: &memorySearchSemanticStatus{Enabled: true},
		Hits:     []memorySearchHit{},
	}
	if len(candidates) == 0 {
		return result, nil
	}
	inputs := make([]string, 0, len(candidates)+1)
	inputs = append(inputs, truncateEmbedText(query))
	for _, c := range candidates {
		inputs = append(inputs, truncateEmbedText(c.Entry))
	}
	resp, err := d.embedWithDeadline(modelrouter.EmbeddingsRequest{Model: modelID, Inputs: inputs})
	if err != nil {
		return memorySearchResult{}, fmt.Errorf("semantic memory search unavailable: %w", err)
	}
	if resp == nil || len(resp.Vectors) != len(inputs) || len(resp.Vectors[0]) == 0 {
		return memorySearchResult{}, fmt.Errorf("semantic memory search unavailable: malformed embeddings response")
	}
	queryVector := resp.Vectors[0]
	for i, candidate := range candidates {
		score, ok := cosineScore(queryVector, resp.Vectors[i+1])
		if !ok {
			continue
		}
		hit := candidate
		hit.Score = score
		hit.Mode = memorySearchModeSemantic
		result.Hits = append(result.Hits, hit)
	}
	sort.SliceStable(result.Hits, func(i, j int) bool {
		if result.Hits[i].Score == result.Hits[j].Score {
			if result.Hits[i].Target == result.Hits[j].Target {
				return result.Hits[i].Index < result.Hits[j].Index
			}
			return result.Hits[i].Target < result.Hits[j].Target
		}
		return result.Hits[i].Score > result.Hits[j].Score
	})
	if len(result.Hits) > limit {
		result.Hits = result.Hits[:limit]
	}
	result.Semantic.Provider = resp.Provider
	result.Semantic.Model = resp.Model
	return result, nil
}

func (d *Daemon) memoryEmbeddingsModelID(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = d.embeddingsModelID()
		if model == "" {
			return "", fmt.Errorf("semantic memory search unavailable: no embeddings provider")
		}
		return model, nil
	}
	if prefix, _, ok := strings.Cut(model, "/"); ok && !d.router.HasEmbeddingsProviderNamed(prefix) {
		return "", fmt.Errorf("semantic memory search unavailable: embeddings provider %q is not registered", prefix)
	}
	if !d.router.HasEmbeddingsProvider() {
		return "", fmt.Errorf("semantic memory search unavailable: no embeddings provider")
	}
	return model, nil
}

func semanticMemoryFallbackReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "no embeddings provider") {
		return "no-provider"
	}
	if strings.Contains(msg, "malformed embeddings response") {
		return "provider-error"
	}
	return "provider-error"
}

func cosineScore(a, b []float32) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, aa, bb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return 0, false
		}
		dot += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0, false
	}
	score := dot / (math.Sqrt(aa) * math.Sqrt(bb))
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0, false
	}
	return score, true
}
