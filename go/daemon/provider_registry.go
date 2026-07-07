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

func loadRuntimeProviderCatalog() provider.Catalog {
	cachePath, err := provider.DefaultCachePath()
	if err != nil {
		return provider.Seed()
	}
	cat, err := provider.Load(provider.Options{CachePath: cachePath, ModelsURL: os.Getenv("CARINA_MODELS_URL")})
	if err != nil || len(cat) == 0 {
		return provider.Seed()
	}
	if os.Getenv("CARINA_PROVIDER_REFRESH") == "1" || os.Getenv("CARINA_PROVIDER_REFRESH") == "true" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if refreshed, err := provider.Refresh(ctx, provider.Options{CachePath: cachePath, ModelsURL: os.Getenv("CARINA_MODELS_URL")}); err == nil {
			return refreshed
		}
	}
	return cat
}

func registerProviders(router *modelrouter.Router, offline bool, store *auth.Store, cat provider.Catalog) {
	if !offline {
		for _, info := range orderedRuntimeProviders(cat) {
			if p := buildRuntimeProvider(info, store); p != nil {
				router.RegisterProvider(p)
			}
		}
	}
	router.RegisterProvider(modelrouter.NewMockProvider())
}

func hasRunnableRuntimeProvider(cat provider.Catalog, store *auth.Store) bool {
	for _, info := range orderedRuntimeProviders(cat) {
		if detectRuntimeProtocol(info) == protocolUnsupported {
			continue
		}
		baseURL, ok := runtimeBaseURL(info)
		if !ok || strings.TrimSpace(baseURL) == "" {
			continue
		}
		if isLocalEndpoint(baseURL) {
			return true
		}
		chain := auth.ProviderChain(normalizeProviderID(info.ID), info.Env, store, nil)
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
	chain := auth.ProviderChain(id, info.Env, store, nil)
	noAuth := isLocalEndpoint(baseURL)
	quirk := runtimeProviderQuirk(id, baseURL)
	overrides := runtimeModelOverrides(info)
	switch protocol {
	case protocolAnthropic:
		return newAnthropicCatalogProvider(id, baseURL, model, chain, quirk.Headers, quirk.Body, overrides)
	case protocolGemini:
		return &geminiProvider{providerBase: providerBase{
			id: id, baseURL: baseURL, defaultModel: model, auth: chain, noAuth: noAuth,
			headers: quirk.Headers, body: quirk.Body, overrides: overrides,
		}}
	case protocolOpenAIResponses, protocolOpenAIChat:
		return &openAIProvider{providerBase: providerBase{
			id: id, baseURL: baseURL, defaultModel: model, auth: chain, noAuth: noAuth,
			headers: quirk.Headers, body: quirk.Body, overrides: overrides,
		}, responses: protocol == protocolOpenAIResponses}
	default:
		return nil
	}
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
	if strings.TrimSpace(info.API) != "" {
		return expandEnvStrict(info.API)
	}
	base, ok := defaultProviderBaseURL[normalizeProviderID(info.ID)]
	return base, ok
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
