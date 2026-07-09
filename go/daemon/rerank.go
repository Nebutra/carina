package daemon

// V3 reranker seam (docs/plans/code-intelligence.md §C): an optional rerank
// stage in the code.search path, applied daemon-side after
// kernel.index.search returns and before rendering — retrieval (kernel, RRF)
// is untouched. V3 ships the seam only: the resolver table is empty, no real
// reranker registers, and tests inject fakes through the configuredReranker
// seam variable (the lspServerForExt pattern). Selection mirrors the
// embeddings model idiom: CARINA_RERANKER unset/empty means the stage is
// skipped bit-identically; an unrecognized value keeps the reranker off
// (observable via one daemon log line, never an error).

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// rerankCandidate is the subset of a search hit a reranker may see.
type rerankCandidate struct {
	Path      string
	StartLine int
	EndLine   int
	Snippet   string
	Score     float64
}

// Reranker reorders candidates. It returns a permutation of candidate
// indices — a reranker can reorder governed results but never inject, drop,
// or rewrite them. Any failure (error, wrong length, duplicate or
// out-of-range index) falls back to the original kernel order.
type Reranker interface {
	Name() string
	Rerank(ctx context.Context, query string, candidates []rerankCandidate) ([]int, error)
}

// configuredReranker resolves the active reranker (nil = stage off); tests
// override it to inject fakes, exactly like lspServerForExt.
var configuredReranker = rerankerFromEnv

// rerankerFromEnv resolves CARINA_RERANKER against the (deliberately empty)
// V3 provider table.
func rerankerFromEnv() Reranker {
	name := strings.TrimSpace(os.Getenv("CARINA_RERANKER"))
	if name == "" {
		return nil
	}
	// The V3 table registers no real reranker, so every configured name is
	// unrecognized: stay off, observably (one log line, never an error).
	fmt.Printf("carina-daemon: unrecognized reranker %q (CARINA_RERANKER); rerank stays off\n", name)
	return nil
}

// validPermutation reports whether perm is a permutation of 0..n-1 — the only
// thing a reranker may return (reorder, never inject/drop/duplicate).
func validPermutation(perm []int, n int) bool {
	if len(perm) != n {
		return false
	}
	seen := make([]bool, n)
	for _, p := range perm {
		if p < 0 || p >= n || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}
