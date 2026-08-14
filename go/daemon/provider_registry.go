package daemon

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

type runtimeProtocol string

const (
	protocolUnsupported     runtimeProtocol = ""
	protocolAnthropic       runtimeProtocol = "anthropic"
	protocolGemini          runtimeProtocol = "gemini"
	protocolOpenAIChat      runtimeProtocol = "openai-chat"
	protocolOpenAIResponses runtimeProtocol = "openai-responses"
	grokBuildActionDisabled                 = "disabled"
	grokBuildDisabledReason                 = "Grok Build is disabled by configuration"
)

var defaultProviderBaseURL = map[string]string{
	"anthropic":  "https://api.anthropic.com/v1",
	"cerebras":   "https://api.cerebras.ai/v1",
	"deepinfra":  "https://api.deepinfra.com/v1/openai",
	"google":     "https://generativelanguage.googleapis.com/v1beta",
	"groq":       "https://api.groq.com/openai/v1",
	"mistral":    "https://api.mistral.ai/v1",
	"openai":     "https://api.openai.com/v1",
	"openrouter": "https://openrouter.ai/api/v1",
	"perplexity": "https://api.perplexity.ai",
	"togetherai": "https://api.together.xyz/v1",
	"xai":        "https://api.x.ai/v1",
}

var defaultProviderModel = map[string]string{
	"anthropic":  "claude-fable-5",
	"google":     "gemini-2.5-pro",
	"groq":       "openai/gpt-oss-120b",
	"mistral":    "mistral-large-latest",
	"openai":     "gpt-5",
	"openrouter": "openai/gpt-5",
	"xai":        "grok-4",
}

var lookupCCSwitchCredential = provider.LookupCCSwitchCredential

var openAICompatibleProviderIDs = map[string]bool{
	"cerebras":   true,
	"deepinfra":  true,
	"groq":       true,
	"mistral":    true,
	"openrouter": true,
	"perplexity": true,
	"togetherai": true,
	"xai":        true,
}

type providerQuirk struct {
	Headers map[string]string
	Body    map[string]json.RawMessage
}

func loadRuntimeProviderCatalog(offline bool, disabled map[string]bool) provider.Catalog {
	grokBuildDisabled := disabled[provider.GrokBuildProviderID]
	cachePath, err := provider.DefaultCachePath()
	if err != nil {
		return mergeLocalProviderDiscoveries(provider.Seed(), !offline && !grokBuildDisabled, grokBuildDisabled)
	}
	strategy := provider.RefreshOnlineIfUncached
	if offline {
		strategy = provider.RefreshOffline
	}
	if os.Getenv("CARINA_PROVIDER_REFRESH") == "1" || os.Getenv("CARINA_PROVIDER_REFRESH") == "true" {
		strategy = provider.RefreshOnline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cat, err := provider.LoadWithStrategy(ctx, provider.Options{CachePath: cachePath, ModelsURL: os.Getenv("CARINA_MODELS_URL")}, strategy)
	if err != nil || len(cat) == 0 {
		return mergeLocalProviderDiscoveries(provider.Seed(), !offline && !grokBuildDisabled, grokBuildDisabled)
	}
	return mergeLocalProviderDiscoveries(mergeBuiltinRuntimeProviders(cat), !offline && !grokBuildDisabled, grokBuildDisabled)
}

func mergeLocalProviderDiscoveries(cat provider.Catalog, discoverGrokBuild, grokBuildDisabled bool) provider.Catalog {
	if grokBuildDisabled {
		cat = mergeDisabledGrokBuildProvider(cat)
	} else if discoverGrokBuild {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		discovery := provider.DetectGrokBuild(ctx)
		cancel()
		cat = provider.MergeGrokBuildProvider(cat, discovery)
	}
	profiles, err := provider.DetectCCSwitchProviders("")
	if err != nil || len(profiles) == 0 {
		return cat
	}
	return provider.MergeCCSwitchProviders(cat, profiles)
}

func mergeDisabledGrokBuildProvider(base provider.Catalog) provider.Catalog {
	merged := make(provider.Catalog, len(base)+1)
	for id, info := range base {
		if normalizeProviderID(id) != provider.GrokBuildProviderID {
			merged[id] = info
		}
	}
	merged[provider.GrokBuildProviderID] = provider.Info{
		ID: provider.GrokBuildProviderID, Name: provider.GrokBuildSourceLabel,
		Source: &provider.Source{
			Kind: provider.GrokBuildSourceKind, Label: provider.GrokBuildSourceLabel, App: "grok",
			Route: provider.GrokBuildRouteCLIOAuth, AuthMode: provider.GrokBuildAuthModeCLIOAuth,
			CredentialOwner: provider.GrokBuildCredentialOwner, Action: grokBuildActionDisabled,
			Rank: -100, Current: false, Importable: false, Reason: grokBuildDisabledReason,
		},
		Models: map[string]provider.Model{},
	}
	return merged
}

func mergeBuiltinRuntimeProviders(cat provider.Catalog) provider.Catalog {
	merged := make(provider.Catalog, len(cat)+len(provider.Seed()))
	for id, info := range provider.Seed() {
		merged[id] = info
	}
	for id, info := range cat {
		merged[id] = info
	}
	return merged
}

func disabledProviderSet(ids []string) map[string]bool {
	disabled := make(map[string]bool, len(ids))
	for _, id := range ids {
		if normalized := normalizeProviderID(id); normalized != "" {
			disabled[normalized] = true
		}
	}
	return disabled
}

func registerProviders(router *modelrouter.Router, offline bool, disabledProviders []string, store *auth.Store, cat provider.Catalog) {
	if !offline {
		disabled := disabledProviderSet(disabledProviders)
		for _, info := range orderedRuntimeProviders(cat) {
			if disabled[normalizeProviderID(info.ID)] {
				continue
			}
			if p := buildRuntimeProvider(info, store); p != nil {
				router.RegisterProvider(p)
			}
		}
	}
	router.RegisterProvider(modelrouter.NewMockProvider())
}

func hasRunnableRuntimeProvider(cat provider.Catalog, disabledProviders []string, store *auth.Store) bool {
	return hasRunnableRuntimeProviderSet(cat, disabledProviderSet(disabledProviders), store)
}

func hasRunnableRuntimeProviderSet(cat provider.Catalog, disabled map[string]bool, store *auth.Store) bool {
	for _, info := range orderedRuntimeProviders(cat) {
		if disabled[normalizeProviderID(info.ID)] {
			continue
		}
		if runtimeProviderUsesGrokBuild(info) {
			if info.Source.Current && info.Source.Action == provider.GrokBuildActionUseSession {
				return true
			}
			continue
		}
		if detectRuntimeProtocol(info) == protocolUnsupported {
			continue
		}
		if !runtimeSourceAllowsExecution(info) {
			continue
		}
		if runtimeProviderUsesClaudeCLI(info) {
			if claudeCLIAvailable() {
				return true
			}
			continue
		}
		baseURL, ok := runtimeBaseURL(info)
		if !ok || strings.TrimSpace(baseURL) == "" {
			continue
		}
		if runtimeProviderAllowsNoAuth(info, baseURL) {
			if _, explicitlyConfigured := runtimeBaseURLOverride(info); explicitlyConfigured {
				return true
			}
		}
		chain := runtimeProviderAuthChain(info, store)
		if cred, ok := chain.Resolve(); ok && strings.TrimSpace(cred.Value) != "" {
			return true
		}
	}
	return false
}

func orderedRuntimeProviders(cat provider.Catalog) []provider.Info {
	priority := []string{"anthropic", "openai", "openrouter", "google"}
	seen := map[string]bool{}
	out := []provider.Info{}
	for _, id := range priority {
		if p, ok := cat[id]; ok {
			p.ID = id
			out = append(out, p)
			seen[id] = true
		}
	}
	for _, p := range provider.Sorted(cat) {
		if !seen[p.ID] {
			out = append(out, p)
			seen[p.ID] = true
		}
	}
	return out
}

func buildRuntimeProvider(info provider.Info, store *auth.Store) modelrouter.Provider {
	id := normalizeProviderID(info.ID)
	if id == "" {
		return nil
	}
	info.ID = id
	if runtimeProviderUsesGrokBuild(info) {
		return nil
	}
	protocol := detectRuntimeProtocol(info)
	if protocol == protocolUnsupported {
		return nil
	}
	baseURL, ok := runtimeBaseURL(info)
	if !ok || strings.TrimSpace(baseURL) == "" {
		return nil
	}
	baseURL = strings.TrimRight(baseURL, "/")
	model := runtimeDefaultModel(info)
	chain := runtimeProviderAuthChain(info, store)
	noAuth := runtimeProviderAllowsNoAuth(info, baseURL)
	quirk := runtimeProviderQuirk(id, baseURL)
	overrides := runtimeModelOverrides(info)
	errorName := runtimeProviderErrorName(info)
	switch protocol {
	case protocolAnthropic:
		return newAnthropicCatalogProvider(id, errorName, baseURL, model, chain, quirk.Headers, quirk.Body, overrides)
	case protocolGemini:
		return &geminiProvider{providerBase: providerBase{
			id: id, label: errorName, baseURL: baseURL, defaultModel: model, auth: chain, noAuth: noAuth,
			headers: quirk.Headers, body: quirk.Body, overrides: overrides,
		}}
	case protocolOpenAIResponses, protocolOpenAIChat:
		return &openAIProvider{providerBase: providerBase{
			id: id, label: errorName, baseURL: baseURL, defaultModel: model, auth: chain, noAuth: noAuth,
			headers: quirk.Headers, body: quirk.Body, overrides: overrides,
		}, responses: protocol == protocolOpenAIResponses}
	default:
		return nil
	}
}

func runtimeProviderErrorName(info provider.Info) string {
	if info.Source != nil {
		if name := strings.TrimSpace(info.Name); name != "" {
			return name
		}
	}
	return normalizeProviderID(info.ID)
}

func runtimeProviderUsesGrokBuild(info provider.Info) bool {
	return info.Source != nil &&
		info.Source.Kind == provider.GrokBuildSourceKind &&
		normalizeProviderID(info.ID) == provider.GrokBuildProviderID
}

func runtimeProviderAuthChain(info provider.Info, store *auth.Store) *auth.Chain {
	sources := make([]auth.Source, 0, len(info.Env)+2)
	for _, env := range info.Env {
		sources = append(sources, auth.EnvKey{Var: env})
	}
	claudeCodeOwned := runtimeProviderUsesClaudeCLI(info)
	if store != nil && !claudeCodeOwned {
		if info.Source != nil && info.Source.Kind == provider.CCSwitchSourceKind {
			sources = append(sources, validatedRuntimeStoreKey{
				store:          store,
				providerID:     info.ID,
				sourceRevision: strings.TrimSpace(info.Source.Revision),
			})
		} else {
			sources = append(sources, auth.StoreKey{Store: store, Provider: info.ID})
		}
	}
	// Importable CC Switch profiles can resolve a reusable token from the local
	// CC Switch DB without a prior `carina auth import`. That unlocks live
	// model lists (TDS, relays) and execution. Claude Code-owned OAuth is omitted
	// because the router delegates that route to the owning CLI instead.
	if info.Source != nil &&
		info.Source.Kind == provider.CCSwitchSourceKind &&
		!claudeCodeOwned &&
		info.Source.Importable &&
		runtimeSourceAllowsExecution(info) {
		sources = append(sources, ccSwitchRuntimeCredential{
			providerID:     info.ID,
			sourceRevision: strings.TrimSpace(info.Source.Revision),
		})
	}
	return auth.NewChain(sources...)
}

// ccSwitchRuntimeCredential resolves secrets straight from CC Switch for
// importable profiles. Values are never logged; Name is safe provenance.
type ccSwitchRuntimeCredential struct {
	providerID     string
	sourceRevision string
}

func (s ccSwitchRuntimeCredential) Name() string {
	return "cc-switch:" + normalizeProviderID(s.providerID)
}

func (s ccSwitchRuntimeCredential) Resolve() (auth.Credential, bool) {
	profile, secret, ok := lookupCCSwitchCredential(s.providerID)
	if !ok || strings.TrimSpace(secret) == "" {
		return auth.Credential{}, false
	}
	if s.sourceRevision != "" && strings.TrimSpace(profile.Revision) != "" && profile.Revision != s.sourceRevision {
		return auth.Credential{}, false
	}
	kind := auth.APIKey
	if profile.CredentialKind == provider.CCSwitchCredentialBearer {
		kind = auth.Bearer
	}
	return auth.Credential{Kind: kind, Value: secret, Source: s.Name()}, true
}

// validatedRuntimeStoreKey keeps imported credentials dynamic without letting
// stale CC Switch metadata become execution authority. Providers are
// registered once, while credential import and revocation can happen later in
// the same daemon process.
type validatedRuntimeStoreKey struct {
	store          *auth.Store
	providerID     string
	sourceRevision string
}

func (s validatedRuntimeStoreKey) Name() string {
	return "auth:" + normalizeProviderID(s.providerID)
}

func (s validatedRuntimeStoreKey) Resolve() (auth.Credential, bool) {
	if s.store == nil {
		return auth.Credential{}, false
	}
	credential, ok, err := s.store.Get(s.providerID)
	if err != nil || !ok || credential.Metadata["validation"] != providerValidationContract {
		return auth.Credential{}, false
	}
	if s.sourceRevision != "" && credential.Metadata["source_revision"] != s.sourceRevision {
		return auth.Credential{}, false
	}
	return auth.StoreKey{Store: s.store, Provider: s.providerID}.Resolve()
}

func runtimeStoredCredentialAllowed(info provider.Info, store *auth.Store) bool {
	if info.Source == nil || info.Source.Kind != provider.CCSwitchSourceKind {
		return true
	}
	credential, ok, err := store.Get(info.ID)
	if err != nil || !ok || credential.Metadata["validation"] != providerValidationContract {
		return false
	}
	revision := strings.TrimSpace(info.Source.Revision)
	return revision == "" || credential.Metadata["source_revision"] == revision
}

func runtimeProviderAllowsNoAuth(info provider.Info, baseURL string) bool {
	if !isLocalEndpoint(baseURL) {
		return false
	}
	return info.Source == nil || info.Source.Kind != provider.CCSwitchSourceKind
}

func runtimeProviderQuirk(id, baseURL string) providerQuirk {
	headers := map[string]string{}
	body := map[string]json.RawMessage{}
	setHeader := func(k, v string) {
		headers[k] = v
	}
	switch id {
	case "openrouter":
		setHeader("HTTP-Referer", "https://github.com/Nebutra/carina")
		setHeader("X-Title", "Carina")
	case "llmgateway":
		setHeader("HTTP-Referer", "https://github.com/Nebutra/carina")
		setHeader("X-Title", "Carina")
		setHeader("X-Source", "Carina")
	case "nvidia":
		setHeader("HTTP-Referer", "https://github.com/Nebutra/carina")
		setHeader("X-Title", "Carina")
		setHeader("X-BILLING-INVOKE-ORIGIN", "Carina")
	case "vercel":
		setHeader("http-referer", "https://github.com/Nebutra/carina")
		setHeader("x-title", "Carina")
	case "zenmux", "kilo":
		setHeader("HTTP-Referer", "https://github.com/Nebutra/carina")
		setHeader("X-Title", "Carina")
	}
	if strings.Contains(baseURL, "openrouter.ai") {
		setHeader("HTTP-Referer", "https://github.com/Nebutra/carina")
		setHeader("X-Title", "Carina")
	}
	return providerQuirk{Headers: headers, Body: body}
}

func runtimeModelOverrides(info provider.Info) map[string]requestOverride {
	out := map[string]requestOverride{}
	for id, model := range info.Models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			modelID = id
		}
		for mode, settings := range model.ExperimentalModes() {
			mode = strings.TrimSpace(mode)
			if mode == "" {
				continue
			}
			alias := modelID + "-" + mode
			ro := requestOverride{Model: modelID}
			if settings.Provider != nil {
				ro.Headers = cloneStringMap(settings.Provider.Headers)
				ro.Body = cloneRawMap(settings.Provider.Body)
			}
			out[alias] = mergeOverride(out[alias], ro)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeOverride(base, next requestOverride) requestOverride {
	if strings.TrimSpace(next.Model) != "" {
		base.Model = next.Model
	}
	if len(next.Headers) > 0 {
		base.Headers = mergeStringMaps(base.Headers, next.Headers)
	}
	if len(next.Body) > 0 {
		base.Body = mergeRawMaps(base.Body, next.Body)
	}
	return base
}

func mergeStringMaps(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func mergeRawMaps(a, b map[string]json.RawMessage) map[string]json.RawMessage {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(a)+len(b))
	for k, v := range a {
		out[k] = append(json.RawMessage(nil), v...)
	}
	for k, v := range b {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneRawMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

func detectRuntimeProtocol(info provider.Info) runtimeProtocol {
	switch strings.TrimSpace(info.APIProtocol) {
	case string(protocolAnthropic):
		return protocolAnthropic
	case string(protocolGemini):
		return protocolGemini
	case string(protocolOpenAIChat):
		return protocolOpenAIChat
	case string(protocolOpenAIResponses):
		return protocolOpenAIResponses
	}
	id := normalizeProviderID(info.ID)
	npm := strings.ToLower(strings.TrimSpace(info.NPM))
	switch {
	case id == "google" || npm == "@ai-sdk/google":
		return protocolGemini
	case id == "anthropic" || npm == "@ai-sdk/anthropic":
		return protocolAnthropic
	case id == "openai":
		return protocolOpenAIResponses
	case id == "openrouter" || strings.Contains(npm, "openai-compatible") || npm == "@ai-sdk/openai" || openAICompatibleProviderIDs[id]:
		return protocolOpenAIChat
	default:
		return protocolUnsupported
	}
}

func runtimeBaseURL(info provider.Info) (string, bool) {
	if value, ok := runtimeBaseURLOverride(info); ok {
		return value, true
	}
	if strings.TrimSpace(info.API) != "" {
		return expandEnvStrict(info.API)
	}
	base, ok := defaultProviderBaseURL[normalizeProviderID(info.ID)]
	return base, ok
}

func runtimeBaseURLOverride(info provider.Info) (string, bool) {
	for _, key := range providerBaseURLEnvCandidates(info) {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return expandEnvStrict(value)
		}
	}
	return "", false
}

func providerBaseURLEnvCandidates(info provider.Info) []string {
	seen := map[string]bool{}
	var out []string
	add := func(key string) {
		key = strings.TrimSpace(key)
		if key != "" && !seen[key] {
			out = append(out, key)
			seen[key] = true
		}
	}
	add(strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(info.ID)) + "_BASE_URL")
	for _, env := range info.Env {
		env = strings.TrimSpace(env)
		switch {
		case strings.HasSuffix(env, "_API_KEY"):
			add(strings.TrimSuffix(env, "_API_KEY") + "_BASE_URL")
		case strings.HasSuffix(env, "_KEY"):
			add(strings.TrimSuffix(env, "_KEY") + "_BASE_URL")
		}
	}
	return out
}

func runtimeDefaultModel(info provider.Info) string {
	for _, key := range modelEnvCandidates(info) {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	if model := defaultProviderModel[normalizeProviderID(info.ID)]; model != "" {
		if len(info.Models) == 0 {
			return model
		}
		if _, ok := info.Models[model]; ok {
			return model
		}
		for _, m := range info.Models {
			if m.ID == model {
				return model
			}
		}
	}
	if model := preferredCatalogModel(info); model != "" {
		return model
	}
	return chooseCatalogModel(info.Models)
}

func preferredCatalogModel(info provider.Info) string {
	id := normalizeProviderID(info.ID)
	preferred := []string{}
	switch id {
	case "anthropic":
		preferred = []string{"claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5"}
	case "openai":
		preferred = []string{"gpt-5", "gpt-5.2-pro", "gpt-4.1"}
	case "google":
		preferred = []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash"}
	case "openrouter":
		preferred = []string{"openai/gpt-5", "anthropic/claude-sonnet-4.5", "google/gemini-2.5-pro"}
	}
	for _, candidate := range preferred {
		if _, ok := info.Models[candidate]; ok {
			return candidate
		}
		for _, m := range info.Models {
			if m.ID == candidate {
				return candidate
			}
		}
	}
	return ""
}

func modelEnvCandidates(info provider.Info) []string {
	seen := map[string]bool{}
	var out []string
	add := func(key string) {
		key = strings.TrimSpace(key)
		if key != "" && !seen[key] {
			out = append(out, key)
			seen[key] = true
		}
	}
	add(strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(info.ID)) + "_MODEL")
	for _, env := range info.Env {
		env = strings.TrimSpace(env)
		switch {
		case strings.HasSuffix(env, "_API_KEY"):
			add(strings.TrimSuffix(env, "_API_KEY") + "_MODEL")
		case strings.HasSuffix(env, "_KEY"):
			add(strings.TrimSuffix(env, "_KEY") + "_MODEL")
		}
	}
	return out
}

func chooseCatalogModel(models map[string]provider.Model) string {
	if len(models) == 0 {
		return "default"
	}
	type scored struct {
		id    string
		score int
	}
	items := make([]scored, 0, len(models))
	for id, model := range models {
		if modelUnsupportedByTextPrompt(id, model) {
			continue
		}
		items = append(items, scored{id: id, score: modelScore(id, model)})
	}
	if len(items) == 0 {
		for id := range models {
			items = append(items, scored{id: id})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].id < items[j].id
		}
		return items[i].score > items[j].score
	})
	return items[0].id
}

func modelScore(id string, model provider.Model) int {
	score := 0
	switch strings.ToLower(strings.TrimSpace(model.Status)) {
	case "", "active":
		score += 1000
	case "beta":
		score += 850
	case "alpha":
		score += 650
	case "deprecated":
		score -= 10000
	}
	if model.Modalities == nil || containsStringFold(model.Modalities.Input, "text") {
		score += 120
	}
	if model.Modalities == nil || containsStringFold(model.Modalities.Output, "text") {
		score += 160
	}
	if model.Reasoning {
		score += 80
	}
	if model.ToolCall {
		score += 50
	}
	if model.Attachment {
		score += 15
	}
	if model.Limit.Context > 0 {
		score += minInt(model.Limit.Context/8000, 60)
	}
	if model.Limit.Output > 0 {
		score += minInt(model.Limit.Output/4000, 40)
	}
	if t, ok := modelReleaseTime(model); ok {
		score += minInt(int(t.Sub(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)).Hours()/24/14), 120)
	}
	name := strings.ToLower(id + " " + model.Name)
	for _, marker := range []string{"preview", "experimental", "beta", "alpha"} {
		if strings.Contains(name, marker) {
			score -= 25
		}
	}
	if model.Cost != nil {
		score -= minInt(int(model.Cost.Input+model.Cost.Output), 80)
	}
	return score
}

func modelReleaseTime(model provider.Model) (time.Time, bool) {
	for _, value := range []string{model.ReleaseDate, model.LastUpdated, model.Knowledge} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if t, err := time.Parse("2006-01-02", value); err == nil {
			return t, true
		}
		if t, err := time.Parse("2006-01", value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func modelUnsupportedByTextPrompt(id string, model provider.Model) bool {
	status := strings.ToLower(strings.TrimSpace(model.Status))
	if status == "deprecated" {
		return true
	}
	name := strings.ToLower(id + " " + model.Name)
	for _, bad := range []string{"embedding", "image", "audio", "tts", "transcribe", "moderation", "rerank"} {
		if strings.Contains(name, bad) {
			return true
		}
	}
	if model.Modalities == nil {
		return false
	}
	if len(model.Modalities.Output) > 0 && !containsStringFold(model.Modalities.Output, "text") {
		return true
	}
	if len(model.Modalities.Input) > 0 && !containsStringFold(model.Modalities.Input, "text") {
		return true
	}
	return false
}

func containsStringFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func normalizeProviderID(id string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(id)), "/")
}

var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandEnvStrict(input string) (string, bool) {
	ok := true
	out := envRefPattern.ReplaceAllStringFunc(input, func(match string) string {
		parts := envRefPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			ok = false
			return ""
		}
		value := os.Getenv(parts[1])
		if strings.TrimSpace(value) == "" {
			ok = false
		}
		return value
	})
	return out, ok
}

func isLocalEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
