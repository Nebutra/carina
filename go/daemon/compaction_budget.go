package daemon

import (
	"strings"

	"github.com/Nebutra/carina/go/provider"
)

const (
	compactionPolicyVersion = "model-window-v1"
	fallbackContextTokens   = 32_000
	minContextTokens        = 8_000
	maxContextTokens        = 2_000_000
	compactionFirePercent   = 80
)

type compactionBudget struct {
	WindowTokens  int
	ReserveTokens int
	TriggerTokens int
	Source        string
}

func resolveCompactionBudget(catalog provider.Catalog, modelID string) compactionBudget {
	window, source := fallbackContextTokens, "fallback-estimate"
	// Compaction needs a sane window; tiny/absurd catalog values fall back.
	if limit, resolved, ok := resolveModelContextLimit(catalog, modelID); ok && validContextWindow(limit) {
		window, source = limit, resolved
	}
	reserve := max(4_000, min(32_000, window/10))
	trigger := max(1, (window-reserve)*compactionFirePercent/100)
	return compactionBudget{WindowTokens: window, ReserveTokens: reserve, TriggerTokens: trigger, Source: source}
}

func splitCatalogModelID(modelID string) (string, string) {
	modelID = strings.TrimSpace(modelID)
	providerID, shortModel, ok := strings.Cut(modelID, "/")
	if !ok {
		return "", modelID
	}
	return normalizeProviderID(providerID), shortModel
}

// resolveModelContextLimit finds a catalog context window for a route.
//
// Order:
//  1. Exact provider/model (catalog)
//  2. Same short model under preferred vendors (catalog-alias) — covers
//     managed proxies like ccswitch-codex-managed-proxy/gpt-5.6-sol where
//     the proxy id is not in models.dev but the model id is.
//  3. Global scan by short model id (catalog-alias)
//
// Fallback 32k is applied by the caller when ok is false.
func resolveModelContextLimit(catalog provider.Catalog, modelID string) (limit int, source string, ok bool) {
	providerID, shortModel := splitCatalogModelID(modelID)
	// Display path accepts any positive, bounded window (tests and small models).
	if limit, found := modelContextLimit(catalog, providerID, shortModel); found && limit > 0 && limit <= maxContextTokens {
		return limit, "catalog", true
	}
	name := shortModel
	if name == "" {
		name = strings.TrimSpace(modelID)
	}
	if name == "" {
		return 0, "", false
	}
	if limit, found := modelContextLimitByModelID(catalog, name); found && limit > 0 && limit <= maxContextTokens {
		return limit, "catalog-alias", true
	}
	return 0, "", false
}

func validContextWindow(limit int) bool {
	return limit >= minContextTokens && limit <= maxContextTokens
}

// Preferred vendors when a managed-proxy provider id is missing from models.dev.
var catalogAliasProviders = []string{
	"openai", "azure", "azure-cognitive-services", "github-copilot", "opencode",
	"anthropic", "google", "xai",
}

func modelContextLimitByModelID(catalog provider.Catalog, modelID string) (int, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return 0, false
	}
	for _, providerID := range catalogAliasProviders {
		if limit, ok := modelContextLimit(catalog, providerID, modelID); ok {
			return limit, true
		}
	}
	for providerID := range catalog {
		if limit, ok := modelContextLimit(catalog, providerID, modelID); ok {
			return limit, true
		}
	}
	return 0, false
}

func applyCompactionBudget(tr *Transcript, catalog provider.Catalog, modelID string) {
	if tr == nil {
		return
	}
	budget := resolveCompactionBudget(catalog, modelID)
	tr.policy.MaxTokens = budget.TriggerTokens
	tr.policy.MaxChars = budget.TriggerTokens * 4
	tr.policy.PolicyVersion = compactionPolicyVersion
	tr.policy.WindowTokens = budget.WindowTokens
	tr.policy.ReserveTokens = budget.ReserveTokens
	tr.policy.MetadataSource = budget.Source
	tr.CompactionBudget = CompactionBudgetSnapshot{
		PolicyVersion: compactionPolicyVersion, WindowTokens: budget.WindowTokens,
		ReserveTokens: budget.ReserveTokens, TriggerTokens: budget.TriggerTokens,
		MetadataSource: budget.Source,
	}
}

func restoreCompactionBudget(tr *Transcript) {
	if tr == nil {
		return
	}
	tr.policy = defaultCompactionPolicy()
	budget := tr.CompactionBudget
	if budget.PolicyVersion == "" || budget.WindowTokens <= 0 || budget.TriggerTokens <= 0 {
		return
	}
	tr.policy.MaxTokens = budget.TriggerTokens
	tr.policy.MaxChars = budget.TriggerTokens * 4
	tr.policy.PolicyVersion = budget.PolicyVersion
	tr.policy.WindowTokens = budget.WindowTokens
	tr.policy.ReserveTokens = budget.ReserveTokens
	tr.policy.MetadataSource = budget.MetadataSource
}
