package daemon

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Nebutra/carina/go/auth"
)

type modelInventoryModel struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name,omitempty"`
	Available              bool              `json:"available"`
	Reasoning              bool              `json:"reasoning"`
	ReasoningOptions       []json.RawMessage `json:"reasoning_options,omitempty"`
	ReasoningEfforts       []string          `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string            `json:"default_reasoning_effort,omitempty"`
}

type modelInventoryProvider struct {
	ID            string                `json:"id"`
	Name          string                `json:"name,omitempty"`
	Registered    bool                  `json:"registered"`
	Available     bool                  `json:"available"`
	AuthSource    string                `json:"auth_source,omitempty"`
	DynamicModels bool                  `json:"dynamic_models"`
	DefaultModel  string                `json:"default_model,omitempty"`
	Models        []modelInventoryModel `json:"models"`
}

type modelInventoryReasoner struct {
	Backend   string `json:"backend,omitempty"`
	Model     string `json:"model,omitempty"`
	Available bool   `json:"available"`
	Explicit  bool   `json:"explicit"`
}

func (d *Daemon) handleModelList(_ json.RawMessage) (any, error) {
	registered := map[string]bool{}
	for _, name := range d.router.ProviderNames() {
		registered[normalizeProviderID(name)] = true
	}
	providers := make([]modelInventoryProvider, 0, len(d.providerCatalog))
	for _, info := range orderedRuntimeProviders(d.providerCatalog) {
		id := normalizeProviderID(info.ID)
		if id == "" || detectRuntimeProtocol(info) == protocolUnsupported {
			continue
		}
		chain := auth.ProviderChain(id, info.Env, d.authStore, nil)
		authSource := chain.ResolvedSource()
		endpoint, hasEndpoint := runtimeBaseURL(info)
		_, explicitEndpoint := runtimeBaseURLOverride(info)
		available := registered[id] && (authSource != "" || (hasEndpoint && isLocalEndpoint(endpoint) && explicitEndpoint))
		row := modelInventoryProvider{
			ID: id, Name: info.Name, Registered: registered[id], Available: available,
			AuthSource: authSource, DynamicModels: len(info.Models) == 0,
			DefaultModel: runtimeDefaultModel(info), Models: []modelInventoryModel{},
		}
		for key, model := range info.Models {
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				modelID = strings.TrimSpace(key)
			}
			if modelID == "" || modelUnsupportedByTextPrompt(modelID, model) {
				continue
			}
			effort := catalogReasoningEffortSpec(id, modelID, model)
			row.Models = append(row.Models, modelInventoryModel{ID: id + "/" + modelID, Name: model.Name, Available: available, Reasoning: model.Reasoning, ReasoningOptions: model.ReasoningOptions, ReasoningEfforts: effort.Options, DefaultReasoningEffort: effort.Default})
		}
		sort.Slice(row.Models, func(i, j int) bool { return row.Models[i].ID < row.Models[j].ID })
		providers = append(providers, row)
	}
	return map[string]any{
		"default_model": modelInventoryDefault(providers),
		"reasoner":      d.modelInventoryReasoner(providers),
		"providers":     providers,
	}, nil
}

func modelInventoryDefault(providers []modelInventoryProvider) string {
	for _, provider := range providers {
		if !provider.Registered || !provider.Available {
			continue
		}
		if model := strings.TrimSpace(provider.DefaultModel); model != "" {
			if !strings.HasPrefix(model, provider.ID+"/") {
				model = provider.ID + "/" + model
			}
			return model
		}
		for _, model := range provider.Models {
			if model.Available && strings.TrimSpace(model.ID) != "" {
				return model.ID
			}
		}
	}
	return ""
}

func (d *Daemon) modelInventoryReasoner(providers []modelInventoryProvider) modelInventoryReasoner {
	backend := strings.TrimSpace(d.reasonerBackend)
	if backend == "" && d.reasoner != nil {
		backend = strings.TrimSpace(d.reasoner.Name())
	}
	available := d.reasoner != nil
	if backend == reasonerBackendRouter {
		available = available && modelInventoryDefault(providers) != ""
	}
	return modelInventoryReasoner{
		Backend: backend, Model: strings.TrimSpace(d.reasonerModel),
		Available: available, Explicit: d.reasonerExplicit,
	}
}
