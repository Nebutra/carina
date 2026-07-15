package daemon

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Nebutra/carina/go/auth"
)

type modelInventoryModel struct {
	ID               string            `json:"id"`
	Name             string            `json:"name,omitempty"`
	Available        bool              `json:"available"`
	Reasoning        bool              `json:"reasoning"`
	ReasoningOptions []json.RawMessage `json:"reasoning_options,omitempty"`
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
		available := registered[id] && ((hasEndpoint && isLocalEndpoint(endpoint)) || authSource != "")
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
			row.Models = append(row.Models, modelInventoryModel{ID: id + "/" + modelID, Name: model.Name, Available: available, Reasoning: model.Reasoning, ReasoningOptions: model.ReasoningOptions})
		}
		sort.Slice(row.Models, func(i, j int) bool { return row.Models[i].ID < row.Models[j].ID })
		providers = append(providers, row)
	}
	return map[string]any{"default_model": "default", "providers": providers}, nil
}
