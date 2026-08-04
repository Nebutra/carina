package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Nebutra/carina/go/provider"
)

type reasoningEffortSpec struct {
	Options []string `json:"options"`
	Default string   `json:"default,omitempty"`
}

// Optional per-model overrides (provider/model or bare model id → options+default).
// Loaded once from CARINA_REASONING_EFFORT_OVERRIDES JSON for ops without code changes.
// Example:
//
//	{"openai/gpt-5.6":{"options":["none","low","medium","high","xhigh","max"],"default":"medium"},
//	 "mox/gpt-5.6-sol":{"options":["low","medium","high"],"default":"medium"}}
var (
	reasoningEffortOverrides     map[string]reasoningEffortSpec
	reasoningEffortOverridesOnce sync.Once
)

func loadReasoningEffortOverrides() map[string]reasoningEffortSpec {
	reasoningEffortOverridesOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("CARINA_REASONING_EFFORT_OVERRIDES"))
		if raw == "" {
			reasoningEffortOverrides = map[string]reasoningEffortSpec{}
			return
		}
		var decoded map[string]reasoningEffortSpec
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			reasoningEffortOverrides = map[string]reasoningEffortSpec{}
			return
		}
		out := make(map[string]reasoningEffortSpec, len(decoded))
		for key, spec := range decoded {
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" || len(spec.Options) == 0 {
				continue
			}
			out[key] = normalizeEffortSpec(spec)
		}
		reasoningEffortOverrides = out
	})
	return reasoningEffortOverrides
}

// resetReasoningEffortOverridesForTest clears the once-loaded override map (tests only).
func resetReasoningEffortOverridesForTest() {
	reasoningEffortOverridesOnce = sync.Once{}
	reasoningEffortOverrides = nil
}

func catalogReasoningEffortSpec(providerID, modelID string, model provider.Model) reasoningEffortSpec {
	if !model.Reasoning {
		return reasoningEffortSpec{}
	}
	providerID = normalizeProviderID(providerID)
	modelID = strings.TrimSpace(modelID)

	// Ops override wins (full model id or provider/model).
	if over, ok := lookupReasoningEffortOverride(providerID, modelID); ok {
		return over
	}

	catalogOpts := extractCatalogEffortValues(model.ReasoningOptions)
	native := nativeReasoningEffortSpec(providerID, modelID)

	// Catalog-declared effort values are the primary product surface when present.
	// Intersect with native when we have a family allowlist; otherwise trust catalog.
	if len(catalogOpts) > 0 {
		if len(native.Options) > 0 {
			return intersectEffortSpecs(native, catalogOpts)
		}
		return reasoningEffortSpec{
			Options: catalogOpts,
			Default: preferEffortDefault(catalogOpts, ""),
		}
	}

	// No catalog options: family native ladder only (may be empty → no UI control).
	return native
}

func lookupReasoningEffortOverride(providerID, modelID string) (reasoningEffortSpec, bool) {
	overrides := loadReasoningEffortOverrides()
	if len(overrides) == 0 {
		return reasoningEffortSpec{}, false
	}
	keys := []string{
		strings.ToLower(providerID + "/" + modelID),
		strings.ToLower(modelID),
	}
	for _, key := range keys {
		if spec, ok := overrides[key]; ok && len(spec.Options) > 0 {
			return spec, true
		}
	}
	return reasoningEffortSpec{}, false
}

func extractCatalogEffortValues(rawOptions []json.RawMessage) []string {
	for _, raw := range rawOptions {
		var option struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		}
		if json.Unmarshal(raw, &option) != nil || option.Type != "effort" || len(option.Values) == 0 {
			continue
		}
		out := make([]string, 0, len(option.Values))
		seen := map[string]bool{}
		for _, value := range option.Values {
			value = normalizeReasoningEffort(value)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func intersectEffortSpecs(native reasoningEffortSpec, catalogOpts []string) reasoningEffortSpec {
	allowed := map[string]bool{}
	for _, value := range native.Options {
		allowed[value] = true
	}
	filtered := []string{}
	for _, value := range catalogOpts {
		value = normalizeReasoningEffort(value)
		if allowed[value] {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == 0 {
		// Catalog and native disagree entirely — prefer native family (safer for API).
		return native
	}
	def := native.Default
	if !containsEffort(filtered, def) {
		def = preferEffortDefault(filtered, native.Default)
	}
	return reasoningEffortSpec{Options: filtered, Default: def}
}

func normalizeEffortSpec(spec reasoningEffortSpec) reasoningEffortSpec {
	opts := make([]string, 0, len(spec.Options))
	seen := map[string]bool{}
	for _, value := range spec.Options {
		value = normalizeReasoningEffort(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		opts = append(opts, value)
	}
	return reasoningEffortSpec{
		Options: opts,
		Default: preferEffortDefault(opts, spec.Default),
	}
}

func preferEffortDefault(options []string, preferred string) string {
	preferred = normalizeReasoningEffort(preferred)
	if preferred != "" && containsEffort(options, preferred) {
		return preferred
	}
	// Prefer a balanced middle when present.
	for _, candidate := range []string{"medium", "high", "low"} {
		if containsEffort(options, candidate) {
			return candidate
		}
	}
	if len(options) == 0 {
		return ""
	}
	return options[0]
}

func containsEffort(options []string, effort string) bool {
	for _, option := range options {
		if option == effort {
			return true
		}
	}
	return false
}

// resolveEffortForModel picks a valid effort when switching models.
// Exact match first, then small semantic remap, then model default.
func resolveEffortForModel(spec reasoningEffortSpec, previous string) string {
	if len(spec.Options) == 0 {
		return ""
	}
	previous = normalizeReasoningEffort(previous)
	if previous != "" && containsEffort(spec.Options, previous) {
		return previous
	}
	if previous != "" {
		if mapped := remapReasoningEffort(previous); mapped != previous && containsEffort(spec.Options, mapped) {
			return mapped
		}
	}
	return preferEffortDefault(spec.Options, spec.Default)
}

// remapReasoningEffort maps near-equivalent labels across vendors.
// Keep this table small and tested — not a full compatibility matrix.
func remapReasoningEffort(effort string) string {
	switch normalizeReasoningEffort(effort) {
	case "xhigh", "extra_high", "extra-high":
		return "max"
	case "max":
		return "xhigh"
	case "minimal", "min":
		return "low"
	case "none", "off":
		return "low"
	default:
		return effort
	}
}

func nativeReasoningEffortSpec(providerID, modelID string) reasoningEffortSpec {
	providerID = normalizeProviderID(providerID)
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	switch reasoningEffortFamily(providerID, modelID) {
	case "openai":
		// Full OpenAI-style ladder; catalog intersection trims unsupported tiers.
		return reasoningEffortSpec{
			Options: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
			Default: "medium",
		}
	case "xai":
		return reasoningEffortSpec{Options: []string{"low", "medium", "high"}, Default: "high"}
	case "anthropic":
		// Adaptive thinking effort (Claude 4.6+ / Opus-5 family). Older thinking
		// models use token budgets and must not share this control.
		if anthropicSupportsAdaptiveEffort(modelID) {
			return reasoningEffortSpec{
				Options: []string{"low", "medium", "high", "xhigh", "max"},
				Default: "high",
			}
		}
	case "google":
		// thinking_level for Gemini 3.x. Gemini 2.5 uses thinking_budget instead.
		if strings.Contains(modelID, "gemini-3") || strings.Contains(modelID, "gemini-3.") {
			if strings.Contains(modelID, "image") {
				return reasoningEffortSpec{Options: []string{"minimal", "high"}, Default: "minimal"}
			}
			return reasoningEffortSpec{
				Options: []string{"minimal", "low", "medium", "high"},
				Default: "high",
			}
		}
	}
	// Unknown family: no discrete effort UI unless catalog supplies options.
	return reasoningEffortSpec{}
}

// reasoningEffortFamily maps catalog and CC Switch runtime IDs onto the native
// effort ladders. Without this, ccswitch-claude-* / ccswitch-codex-* live-model
// rows land with reasoning=true but empty reasoning_efforts, so the TUI cannot
// offer Tab effort selection after model pick.
func reasoningEffortFamily(providerID, modelID string) string {
	providerID = normalizeProviderID(providerID)
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	switch providerID {
	case "openai", "openrouter", "azure", "azure-openai":
		return "openai"
	case "xai":
		return "xai"
	case "anthropic":
		return "anthropic"
	case "google", "gemini":
		return "google"
	}
	// CC Switch / managed runtime IDs carry the app in the id prefix.
	// Do not guess from model id alone — custom relays may declare their own
	// catalog effort values that must not be intersected with a wrong family.
	switch {
	case strings.HasPrefix(providerID, "ccswitch-claude"):
		return "anthropic"
	case strings.HasPrefix(providerID, "ccswitch-codex"):
		return "openai"
	case strings.HasPrefix(providerID, "ccswitch-gemini"):
		return "google"
	}
	_ = modelID
	return ""
}

func anthropicSupportsAdaptiveEffort(modelID string) bool {
	modelID = strings.ToLower(modelID)
	// Explicit generation markers for adaptive effort / thinking.level API.
	for _, marker := range []string{
		"4-6", "4.6", "4-7", "4.7", "4-8", "4.8",
		"opus-5", "sonnet-5", "haiku-5", "fable-5",
	} {
		if strings.Contains(modelID, marker) {
			return true
		}
	}
	return false
}

func normalizeReasoningEffort(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func validateReasoningEffort(spec reasoningEffortSpec, effort string) (string, error) {
	effort = normalizeReasoningEffort(effort)
	if effort == "" {
		return "", nil
	}
	for _, option := range spec.Options {
		if effort == option {
			return effort, nil
		}
	}
	if len(spec.Options) == 0 {
		return "", fmt.Errorf("reasoning effort is not supported by the selected provider/model")
	}
	return "", fmt.Errorf("reasoning effort %q is invalid; supported values: %s", effort, strings.Join(spec.Options, ", "))
}

func (d *Daemon) reasoningEffortSpec(model string) reasoningEffortSpec {
	providerID, modelID, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok || providerID == "" || modelID == "" {
		return reasoningEffortSpec{}
	}
	info, ok := d.providerCatalog[normalizeProviderID(providerID)]
	if !ok {
		// Still honor explicit overrides and native family when catalog row is missing
		// (dynamic providers / incomplete seed).
		if over, found := lookupReasoningEffortOverride(providerID, modelID); found {
			return over
		}
		return nativeReasoningEffortSpec(providerID, modelID)
	}
	entry, ok := info.Models[modelID]
	if !ok {
		for key, candidate := range info.Models {
			id := strings.TrimSpace(candidate.ID)
			if id == "" {
				id = key
			}
			if id == modelID {
				entry, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		if over, found := lookupReasoningEffortOverride(providerID, modelID); found {
			return over
		}
		// Dynamic model not in catalog: family native only if reasoning is unknown.
		return nativeReasoningEffortSpec(providerID, modelID)
	}
	return catalogReasoningEffortSpec(providerID, modelID, entry)
}
