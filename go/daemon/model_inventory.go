package daemon

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/provider"
)

type modelInventoryParams struct {
	SessionID string `json:"session_id"`
	ModelID   string `json:"model_id"`
	Locale    string `json:"locale"`
	Refresh   bool   `json:"refresh,omitempty"`
}

type modelInventoryModel struct {
	ID        string `json:"id"`
	DisplayID string `json:"display_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Available bool   `json:"available"`
	// Status is operator-facing route health: ready|circuit_open|rate_limited|
	// probing|auth_error|unavailable. Distinct from Available (credential/route).
	Status                 string            `json:"status,omitempty"`
	StatusReason           string            `json:"status_reason,omitempty"`
	Reasoning              bool              `json:"reasoning"`
	ReasoningOptions       []json.RawMessage `json:"reasoning_options,omitempty"`
	ReasoningEfforts       []string          `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string            `json:"default_reasoning_effort,omitempty"`
	ImageInput             bool              `json:"image_input"`
	ToolCall               bool              `json:"tool_call"`
}

type modelInventoryProvider struct {
	ID                    string `json:"id"`
	Name                  string `json:"name,omitempty"`
	Registered            bool   `json:"registered"`
	Available             bool   `json:"available"`
	AuthSource            string `json:"auth_source,omitempty"`
	SourceKind            string `json:"source_kind,omitempty"`
	SourceLabel           string `json:"source_label,omitempty"`
	SourceApp             string `json:"source_app,omitempty"`
	SourceRoute           string `json:"source_route,omitempty"`
	SourceAuthMode        string `json:"source_auth_mode,omitempty"`
	SourceCredentialOwner string `json:"source_credential_owner,omitempty"`
	SourceAction          string `json:"source_action,omitempty"`
	SourceCurrent         bool   `json:"source_current,omitempty"`
	SourceImportable      bool   `json:"source_importable,omitempty"`
	SourceReason          string `json:"source_reason,omitempty"`
	sourceRank            int
	DynamicModels         bool                  `json:"dynamic_models"`
	DefaultModel          string                `json:"default_model,omitempty"`
	Models                []modelInventoryModel `json:"models"`
}

type modelInventoryReasoner struct {
	Backend   string `json:"backend,omitempty"`
	Model     string `json:"model,omitempty"`
	Available bool   `json:"available"`
	Explicit  bool   `json:"explicit"`
}

type modelInventoryReadiness struct {
	Step       string   `json:"step"`
	Blockers   []string `json:"blockers"`
	RouteKind  string   `json:"route_kind,omitempty"`
	ModelID    string   `json:"model_id,omitempty"`
	Locale     string   `json:"locale,omitempty"`
	CanSubmit  bool     `json:"can_submit"`
	Epoch      string   `json:"epoch,omitempty"`
	Generation uint64   `json:"generation"`
}

func (d *Daemon) handleModelList(params json.RawMessage) (any, error) {
	var request modelInventoryParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
	}
	registered := map[string]bool{}
	for _, name := range d.router.ProviderNames() {
		registered[normalizeProviderID(name)] = true
	}
	catalog := d.providerCatalog
	grokBuildDisabled := d.disabledProviders[provider.GrokBuildProviderID]
	if grokBuildDisabled {
		catalog = mergeDisabledGrokBuildProvider(catalog)
	} else if !d.offline {
		if request.Refresh {
			provider.InvalidateGrokBuildDiscovery()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		discovery := provider.DetectGrokBuild(ctx)
		cancel()
		catalog = provider.MergeGrokBuildProvider(catalog, discovery)
	}
	providers := make([]modelInventoryProvider, 0, len(catalog))
	for _, info := range orderedRuntimeProviders(catalog) {
		id := normalizeProviderID(info.ID)
		grokBuild := runtimeProviderUsesGrokBuild(info)
		if id == "" || (!grokBuild && detectRuntimeProtocol(info) == protocolUnsupported) {
			continue
		}
		chain := runtimeProviderAuthChain(info, d.authStore)
		authSource := chain.ResolvedSource()
		cliDelegated := runtimeProviderUsesClaudeCLI(info) && claudeCLIAvailable()
		if cliDelegated {
			authSource = reasonerBackendClaudeCLI
		}
		endpoint, hasEndpoint := runtimeBaseURL(info)
		_, explicitEndpoint := runtimeBaseURLOverride(info)
		// Stale BYOK must not make explain_unavailable / non-importable CC Switch
		// routes look runnable (that stranded TUI in Diagnostic with zero models).
		providerRegistered := registered[id]
		available := providerRegistered &&
			runtimeSourceAllowsExecution(info) &&
			(cliDelegated || authSource != "" || (hasEndpoint && explicitEndpoint && runtimeProviderAllowsNoAuth(info, endpoint)))
		if grokBuild {
			providerRegistered = !d.disabledProviders[id] && d.canExecuteDedicatedProviderRoute(id)
			available = providerRegistered && info.Source.Current && info.Source.Action == provider.GrokBuildActionUseSession
			if available {
				authSource = reasonerBackendGrokCLI
			}
		}
		row := modelInventoryProvider{
			ID: id, Name: info.Name, Registered: providerRegistered, Available: available,
			AuthSource: authSource, DynamicModels: grokBuild || len(info.Models) == 0,
			DefaultModel: inventoryProviderDefaultModel(info), Models: []modelInventoryModel{},
		}
		if info.Source != nil {
			row.SourceKind = info.Source.Kind
			row.SourceLabel = info.Source.Label
			row.SourceApp = info.Source.App
			row.SourceRoute = info.Source.Route
			row.SourceAuthMode = info.Source.AuthMode
			row.SourceCredentialOwner = info.Source.CredentialOwner
			row.SourceAction = info.Source.Action
			row.SourceCurrent = info.Source.Current
			row.SourceImportable = info.Source.Importable
			row.SourceReason = info.Source.Reason
			row.sourceRank = info.Source.Rank
		}
		// Prefer a live GET /models whenever a credential is resolvable (auth
		// store or live CC Switch DB). Catalog stays the fallback when the
		// endpoint is offline or returns nothing useful.
		if !grokBuild {
			if liveIDs, source := d.liveModelIDs(info, chain); len(liveIDs) > 0 && source != "" {
				row.Models = projectInventoryModels(id, info, available, liveIDs, info.Models)
				row.Models = ensureDefaultModelPresent(row.Models, id, info, available, row.DefaultModel)
				row.DynamicModels = true
				sortInventoryModels(row.Models, id, row.DefaultModel)
				providers = append(providers, row)
				continue
			}
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
		sortInventoryModels(row.Models, id, row.DefaultModel)
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
	for i := range providers {
		d.enrichInventoryModelHealth(providers[i].Models)
	}
	reasoner := d.modelInventoryReasoner(providers)
	readiness := d.modelInventoryReadiness(request, providers, reasoner)
	if d.journey != nil {
		d.journey.observeReady(readiness.CanSubmit)
	}
	return map[string]any{
		"default_model": modelInventoryDefault(providers),
		"reasoner":      reasoner,
		"providers":     providers,
		"readiness":     readiness,
	}, nil
}

// enrichInventoryModelHealth attaches circuit/rate-limit status from runtime
// governance so the model picker can show honesty before submit.
func (d *Daemon) enrichInventoryModelHealth(models []modelInventoryModel) {
	for i := range models {
		if !models[i].Available {
			models[i].Status = "unavailable"
			if models[i].StatusReason == "" {
				models[i].StatusReason = "Provider credential or route is not ready."
			}
			continue
		}
		if d == nil || d.retryGovernance == nil {
			models[i].Status = "ready"
			continue
		}
		// model-router isolates governance by provider/model route id.
		health := d.retryGovernance.routeHealth(models[i].ID)
		models[i].Status = health.Status
		models[i].StatusReason = health.Reason
	}
}

func (d *Daemon) modelInventoryReadiness(request modelInventoryParams, providers []modelInventoryProvider, reasoner modelInventoryReasoner) modelInventoryReadiness {
	snapshot := modelInventoryReadiness{
		Step: "locale", Blockers: []string{}, Generation: d.readinessGeneration.Add(1),
	}
	d.runtimeMu.Lock()
	if d.runtimeLease != nil {
		snapshot.Epoch = d.runtimeLease.state.InstanceID
	}
	d.runtimeMu.Unlock()

	locale := strings.TrimSpace(request.Locale)
	if locale == "" {
		snapshot.Blockers = append(snapshot.Blockers, "locale_required")
	} else if canonical, err := microcopy.CanonicalLocale(locale); err != nil {
		snapshot.Blockers = append(snapshot.Blockers, "locale_unsupported")
	} else {
		snapshot.Locale = canonical
	}

	modelID := strings.TrimSpace(request.ModelID)
	var sessionActive bool
	if sessionID := strings.TrimSpace(request.SessionID); sessionID == "" {
		snapshot.Blockers = append(snapshot.Blockers, "session_required")
	} else if session, ok := d.store.Get(sessionID); !ok || session.Status != "active" {
		snapshot.Blockers = append(snapshot.Blockers, "session_unavailable")
	} else {
		sessionActive = true
		if modelID == "" {
			modelID = strings.TrimSpace(session.NextModel)
		}
	}
	if modelID == "" || modelID == "default" {
		modelID = modelInventoryDefault(providers)
	}
	snapshot.ModelID = modelID

	providerReady := false
	selectedProviderReady := false
	modelReady := false
	for _, provider := range providers {
		if !provider.Registered || !provider.Available {
			continue
		}
		providerReady = true
		for _, model := range provider.Models {
			if model.ID != modelID {
				continue
			}
			selectedProviderReady = true
			modelReady = model.Available
			snapshot.RouteKind = inventoryRouteKind(provider, reasoner)
			break
		}
	}
	if !reasoner.Available || !providerReady {
		snapshot.Blockers = append(snapshot.Blockers, "provider_unavailable")
	}
	if modelID == "" || !selectedProviderReady || !modelReady {
		snapshot.Blockers = append(snapshot.Blockers, "model_unavailable")
	}

	switch {
	case snapshot.Locale == "":
		snapshot.Step = "locale"
	case !reasoner.Available || !providerReady:
		snapshot.Step = "provider"
	case !selectedProviderReady || !modelReady:
		snapshot.Step = "model"
	case !sessionActive:
		snapshot.Step = "session"
	default:
		snapshot.Step = "conversation"
		snapshot.CanSubmit = true
	}
	return snapshot
}

func inventoryRouteKind(row modelInventoryProvider, reasoner modelInventoryReasoner) string {
	if reasoner.Backend == reasonerBackendClaudeCLI || reasoner.Backend == reasonerBackendCodexCLI {
		return "cli_oauth"
	}
	if row.SourceCredentialOwner == provider.CCSwitchCredentialOwnerClaudeCode {
		return "cli_oauth"
	}
	if row.SourceKind == provider.GrokBuildSourceKind {
		return "cli_oauth"
	}
	if row.SourceRoute == providerRouteManagedProxy {
		return "live_proxy"
	}
	if row.AuthSource != "" || row.SourceAuthMode != "" {
		return "credential_source"
	}
	return "upstream_record"
}

const providerRouteManagedProxy = "managed_proxy"

func providerInventoryRank(row modelInventoryProvider) int {
	if row.Registered && row.Available {
		return 0
	}
	if row.SourceKind == provider.GrokBuildSourceKind {
		return 5
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
		if model := strings.TrimSpace(provider.DefaultModel); model != "" && !isPlaceholderModelID(model) {
			if !strings.HasPrefix(model, provider.ID+"/") {
				model = provider.ID + "/" + model
			}
			return model
		}
		for _, model := range provider.Models {
			if model.Available && strings.TrimSpace(model.ID) != "" && !isPlaceholderModelID(model.ID) {
				return model.ID
			}
		}
	}
	return ""
}

// runtimeSourceAllowsExecution reports whether source metadata permits treating
// a resolved credential as execution authority. CC Switch explain_unavailable
// and non-importable rows stay discoverable but never runnable.
func runtimeSourceAllowsExecution(info provider.Info) bool {
	if info.Source == nil {
		return true
	}
	if info.Source.Kind != provider.CCSwitchSourceKind {
		return true
	}
	if !info.Source.Importable {
		return false
	}
	if info.Source.Action == provider.CCSwitchActionExplainUnavailable {
		return false
	}
	return true
}

func inventoryProviderDefaultModel(info provider.Info) string {
	if info.Source != nil {
		if model := strings.TrimSpace(info.Source.DefaultModel); model != "" && !isPlaceholderModelID(model) {
			return model
		}
	}
	model := strings.TrimSpace(runtimeDefaultModel(info))
	if isPlaceholderModelID(model) {
		return ""
	}
	return model
}

func isPlaceholderModelID(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return true
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", "default", "auto", "none":
		return true
	default:
		return false
	}
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
