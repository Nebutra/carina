package daemon

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Nebutra/carina/go/provider"
)

type modelInventoryModel struct {
	ID                     string            `json:"id"`
	DisplayID              string            `json:"display_id,omitempty"`
	Name                   string            `json:"name,omitempty"`
	Available              bool              `json:"available"`
	Reasoning              bool              `json:"reasoning"`
	ReasoningOptions       []json.RawMessage `json:"reasoning_options,omitempty"`
	ReasoningEfforts       []string          `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string            `json:"default_reasoning_effort,omitempty"`
	ImageInput             bool              `json:"image_input"`
	ToolCall               bool              `json:"tool_call"`
}

type modelInventoryProvider struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	Registered       bool   `json:"registered"`
	Available        bool   `json:"available"`
	AuthSource       string `json:"auth_source,omitempty"`
	SourceKind       string `json:"source_kind,omitempty"`
	SourceLabel      string `json:"source_label,omitempty"`
	SourceApp        string `json:"source_app,omitempty"`
	SourceRoute      string `json:"source_route,omitempty"`
	SourceAuthMode   string `json:"source_auth_mode,omitempty"`
	SourceAction     string `json:"source_action,omitempty"`
	SourceCurrent    bool   `json:"source_current,omitempty"`
	SourceImportable bool   `json:"source_importable,omitempty"`
	SourceReason     string `json:"source_reason,omitempty"`
	sourceRank       int
	DynamicModels    bool                  `json:"dynamic_models"`
	DefaultModel     string                `json:"default_model,omitempty"`
	Models           []modelInventoryModel `json:"models"`
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
		chain := runtimeProviderAuthChain(info, d.authStore)
		authSource := chain.ResolvedSource()
		endpoint, hasEndpoint := runtimeBaseURL(info)
		_, explicitEndpoint := runtimeBaseURLOverride(info)
		available := registered[id] && (authSource != "" || (hasEndpoint && explicitEndpoint && runtimeProviderAllowsNoAuth(info, endpoint)))
		row := modelInventoryProvider{
			ID: id, Name: info.Name, Registered: registered[id], Available: available,
			AuthSource: authSource, DynamicModels: len(info.Models) == 0,
			DefaultModel: runtimeDefaultModel(info), Models: []modelInventoryModel{},
		}
		if info.Source != nil {
			row.SourceKind = info.Source.Kind
			row.SourceLabel = info.Source.Label
			row.SourceApp = info.Source.App
			row.SourceRoute = info.Source.Route
			row.SourceAuthMode = info.Source.AuthMode
			row.SourceAction = info.Source.Action
			row.SourceCurrent = info.Source.Current
			row.SourceImportable = info.Source.Importable
			row.SourceReason = info.Source.Reason
			row.sourceRank = info.Source.Rank
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
			displayID := id + "/" + modelID
			if info.Source != nil {
				displayID = info.Name + " / " + modelID
			}
			row.Models = append(row.Models, modelInventoryModel{
				ID: id + "/" + modelID, DisplayID: displayID, Name: model.Name, Available: available,
				Reasoning: model.Reasoning, ReasoningOptions: model.ReasoningOptions,
				ReasoningEfforts: effort.Options, DefaultReasoningEffort: effort.Default,
				ImageInput: modelSupportsImageInput(model), ToolCall: model.ToolCall,
			})
		}
		sort.Slice(row.Models, func(i, j int) bool { return row.Models[i].ID < row.Models[j].ID })
		providers = append(providers, row)
	}
	sort.SliceStable(providers, func(i, j int) bool {
		left, right := providerInventoryRank(providers[i]), providerInventoryRank(providers[j])
		if left != right {
			return left < right
		}
		if providers[i].SourceKind == provider.CCSwitchSourceKind && providers[j].SourceKind == provider.CCSwitchSourceKind {
			if providers[i].sourceRank != providers[j].sourceRank {
				return providers[i].sourceRank < providers[j].sourceRank
			}
			if providers[i].Name != providers[j].Name {
				return providers[i].Name < providers[j].Name
			}
			return providers[i].ID < providers[j].ID
		}
		return false
	})
	return map[string]any{
		"default_model": modelInventoryDefault(providers),
		"reasoner":      d.modelInventoryReasoner(providers),
		"providers":     providers,
	}, nil
}

func providerInventoryRank(row modelInventoryProvider) int {
	if row.Registered && row.Available {
		return 0
	}
	if row.SourceKind == provider.CCSwitchSourceKind {
		switch row.SourceRoute {
		case provider.CCSwitchRouteManagedProxy:
			if row.SourceImportable {
				return 10
			}
			return 20
		case provider.CCSwitchRouteSavedProfile:
			if row.SourceImportable {
				return 30
			}
			return 40
		}
	}
	return 50
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
	available := d.reasonerReady()
	if backend == reasonerBackendRouter {
		available = available && modelInventoryDefault(providers) != ""
	}
	return modelInventoryReasoner{
		Backend: backend, Model: strings.TrimSpace(d.reasonerModel),
		Available: available, Explicit: d.reasonerExplicit,
	}
}
