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
	providerID, shortModel := splitCatalogModelID(modelID)
	if limit, ok := modelContextLimit(catalog, providerID, shortModel); ok && limit >= minContextTokens && limit <= maxContextTokens {
		window, source = limit, "catalog"
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
