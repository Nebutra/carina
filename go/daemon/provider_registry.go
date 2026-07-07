package daemon

import (
	"context"
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
	switch protocol {
	case protocolAnthropic:
		return newAnthropicCatalogProvider(id, baseURL, model, chain)
	case protocolGemini:
		return &geminiProvider{providerBase: providerBase{
			id: id, baseURL: baseURL, defaultModel: model, auth: chain, noAuth: noAuth,
		}}
	case protocolOpenAIResponses, protocolOpenAIChat:
		headers := map[string]string{}
		if id == "openrouter" {
			headers["HTTP-Referer"] = "https://github.com/Nebutra/carina"
			headers["X-Title"] = "Carina"
		}
		return &openAIProvider{providerBase: providerBase{
			id: id, baseURL: baseURL, defaultModel: model, auth: chain, noAuth: noAuth, headers: headers,
		}, responses: protocol == protocolOpenAIResponses}
	default:
		return nil
	}
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
		return model
	}
	return chooseCatalogModel(info.Models)
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
	keys := make([]string, 0, len(models))
	for id, model := range models {
		if modelUnsupportedByTextPrompt(id, model) {
			continue
		}
		keys = append(keys, id)
	}
	if len(keys) == 0 {
		for id := range models {
			keys = append(keys, id)
		}
	}
	sort.Strings(keys)
	return keys[0]
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
