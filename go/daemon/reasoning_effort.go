package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/provider"
)

type reasoningEffortSpec struct {
	Options []string `json:"options"`
	Default string   `json:"default,omitempty"`
}

func catalogReasoningEffortSpec(providerID, modelID string, model provider.Model) reasoningEffortSpec {
	if !model.Reasoning {
		return reasoningEffortSpec{}
	}
	native := nativeReasoningEffortSpec(providerID, modelID)
	if len(native.Options) == 0 {
		return native
	}
	for _, raw := range model.ReasoningOptions {
		var option struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		}
		if json.Unmarshal(raw, &option) != nil || option.Type != "effort" || len(option.Values) == 0 {
			continue
		}
		allowed := map[string]bool{}
		for _, value := range native.Options {
			allowed[value] = true
		}
		filtered := []string{}
		for _, value := range option.Values {
			value = normalizeReasoningEffort(value)
			if allowed[value] {
				filtered = append(filtered, value)
			}
		}
		if len(filtered) > 0 {
			native.Options = filtered
			if !containsEffort(filtered, native.Default) {
				native.Default = filtered[0]
			}
		}
		break
	}
	return native
}

func containsEffort(options []string, effort string) bool {
	for _, option := range options {
		if option == effort {
			return true
		}
	}
	return false
}

func nativeReasoningEffortSpec(providerID, modelID string) reasoningEffortSpec {
	providerID = normalizeProviderID(providerID)
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	switch providerID {
	case "openai", "openrouter":
		return reasoningEffortSpec{Options: []string{"minimal", "low", "medium", "high", "xhigh"}, Default: "medium"}
	case "anthropic":
		// Adaptive thinking effort is a Claude 4.6 API capability. Older
		// thinking models use token budgets, which is a different contract.
		if strings.Contains(modelID, "4-6") || strings.Contains(modelID, "4.6") {
			return reasoningEffortSpec{Options: []string{"low", "medium", "high", "max"}, Default: "high"}
		}
	case "google":
		// thinkingLevel is native to Gemini 3. Gemini 2.5 exposes a token
		// budget instead, so it must not be presented as the same control.
		if strings.Contains(modelID, "gemini-3") {
			return reasoningEffortSpec{Options: []string{"low", "medium", "high"}, Default: "high"}
		}
	}
	return reasoningEffortSpec{}
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
		return reasoningEffortSpec{}
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
		return reasoningEffortSpec{}
	}
	return catalogReasoningEffortSpec(providerID, modelID, entry)
}
