package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/provider"
)

// Live model discovery for runnable providers (managed proxies, OpenAI-compatible
// gateways, Anthropic, etc.). Catalog remains fallback when the endpoint is
// unreachable or returns nothing useful.

const (
	liveModelsTimeout  = 4 * time.Second
	liveModelsDialTTL  = 1500 * time.Millisecond
	liveModelsCacheTTL = 90 * time.Second
	liveModelsMaxBody  = 4 << 20
)

// defaultLiveModelsHTTP fails fast on dead/misconfigured endpoints so model.list
// stays snappy when live discovery is unavailable.
var defaultLiveModelsHTTP = &http.Client{
	Timeout: liveModelsTimeout,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   liveModelsDialTTL,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   liveModelsDialTTL,
		ResponseHeaderTimeout: liveModelsTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

type liveModelsCacheEntry struct {
	ids       []string
	fetchedAt time.Time
	err       string
}

func supportsLiveModelList(protocol runtimeProtocol) bool {
	switch protocol {
	case protocolOpenAIChat, protocolOpenAIResponses, protocolAnthropic, protocolGemini:
		return true
	default:
		return false
	}
}

// prefersLiveModelList reports whether model.list should attempt a live
// GET /models for this provider. Full static catalogs (openai/anthropic/…)
// already ship a rich inventory; live discovery is for thin catalogs and
// imported/managed proxies (CC Switch / TDS / custom gateways with only a
// profile default model).
func prefersLiveModelList(info provider.Info) bool {
	if info.Source != nil {
		return true
	}
	return len(info.Models) <= 1
}

func (d *Daemon) liveModelIDs(info provider.Info, chain *auth.Chain) (ids []string, source string) {
	protocol := detectRuntimeProtocol(info)
	if !supportsLiveModelList(protocol) || !prefersLiveModelList(info) {
		return nil, ""
	}
	endpoint, ok := runtimeBaseURL(info)
	if !ok || strings.TrimSpace(endpoint) == "" {
		return nil, ""
	}
	if chain == nil {
		return nil, ""
	}
	cred, ok := chain.Resolve()
	if !ok || strings.TrimSpace(cred.Value) == "" {
		return nil, ""
	}

	cacheKey := normalizeProviderID(info.ID) + "\x00" + strings.TrimRight(endpoint, "/")
	if cached, hit, _ := d.getLiveModelsCache(cacheKey); hit {
		if len(cached) > 0 {
			return cached, "cache"
		}
		// Negative cache (empty list or prior error): do not hammer every model.list.
		return nil, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), liveModelsTimeout)
	defer cancel()
	ids, err := fetchLiveModelIDs(ctx, protocol, endpoint, cred, d.liveModelsHTTPClient())
	d.putLiveModelsCache(cacheKey, ids, err)
	if err != nil || len(ids) == 0 {
		return nil, ""
	}
	return ids, "live"
}

func (d *Daemon) getLiveModelsCache(key string) (ids []string, hit bool, errMsg string) {
	d.liveModelsMu.Lock()
	defer d.liveModelsMu.Unlock()
	if d.liveModelsCache == nil {
		return nil, false, ""
	}
	entry, ok := d.liveModelsCache[key]
	if !ok || time.Since(entry.fetchedAt) > liveModelsCacheTTL {
		return nil, false, ""
	}
	return append([]string(nil), entry.ids...), true, entry.err
}

func (d *Daemon) putLiveModelsCache(key string, ids []string, err error) {
	d.liveModelsMu.Lock()
	defer d.liveModelsMu.Unlock()
	if d.liveModelsCache == nil {
		d.liveModelsCache = make(map[string]liveModelsCacheEntry)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	d.liveModelsCache[key] = liveModelsCacheEntry{
		ids:       append([]string(nil), ids...),
		fetchedAt: time.Now(),
		err:       msg,
	}
}

func modelsListURL(protocol runtimeProtocol, baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch protocol {
	case protocolGemini:
		// generativelanguage.googleapis.com/v1beta + /models
		if strings.HasSuffix(base, "/v1beta") || strings.Contains(base, "/v1beta/") {
			return strings.TrimRight(base, "/") + "/models"
		}
		return base + "/v1beta/models"
	default:
		if strings.HasSuffix(base, "/v1") || strings.HasSuffix(base, "/openai/v1") {
			return base + "/models"
		}
		return base + "/v1/models"
	}
}

func applyLiveModelsAuth(req *http.Request, protocol runtimeProtocol, cred auth.Credential) {
	switch protocol {
	case protocolAnthropic:
		// Match provider validation: API key -> x-api-key; bearer/oauth -> Authorization.
		cred.Apply(req.Header)
		req.Header.Set("anthropic-version", "2023-06-01")
	case protocolGemini:
		req.Header.Set("x-goog-api-key", cred.Value)
	default:
		// OpenAI-compatible gateways (TDS, CC Switch managed proxy, custom /v1)
		// expect Bearer for both API keys and tokens.
		req.Header.Set("Authorization", "Bearer "+cred.Value)
	}
}

func (d *Daemon) liveModelsHTTPClient() *http.Client {
	if d != nil && d.liveModelsHTTP != nil {
		return d.liveModelsHTTP
	}
	return defaultLiveModelsHTTP
}

func fetchLiveModelIDs(ctx context.Context, protocol runtimeProtocol, baseURL string, cred auth.Credential, client *http.Client) ([]string, error) {
	url := modelsListURL(protocol, baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	applyLiveModelsAuth(req, protocol, cred)

	if client == nil {
		client = defaultLiveModelsHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, liveModelsMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > liveModelsMaxBody {
		return nil, fmt.Errorf("models list response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("models list HTTP %d", resp.StatusCode)
	}
	return parseLiveModelIDs(body)
}

func parseLiveModelIDs(raw []byte) ([]string, error) {
	// OpenAI: {"data":[{"id":"..."}]}
	// Some proxies: {"models":[{"id":"..."}]} or {"models":["..."]}
	// Gemini: {"models":[{"name":"models/gemini-..."}]}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode models list: %w", err)
	}
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		id = normalizeLiveModelID(id)
		if id == "" || seen[id] {
			return
		}
		// Filter non-chat modalities by id heuristics (catalog metadata may be absent).
		if modelUnsupportedByTextPrompt(id, provider.Model{ID: id, Name: id}) {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, item := range payload.Data {
		add(item.ID)
	}
	if len(payload.Models) > 0 {
		var asObjects []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(payload.Models, &asObjects) == nil {
			for _, item := range asObjects {
				if item.ID != "" {
					add(item.ID)
				} else {
					add(item.Name)
				}
			}
		} else {
			var asStrings []string
			if json.Unmarshal(payload.Models, &asStrings) == nil {
				for _, id := range asStrings {
					add(id)
				}
			}
		}
	}
	return ids, nil
}

func normalizeLiveModelID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "models/")
	return strings.TrimSpace(id)
}

// projectInventoryModels builds inventory rows from a preferred id list,
// enriching with catalog metadata when present.
func projectInventoryModels(
	providerID string,
	info provider.Info,
	available bool,
	ids []string,
	catalog map[string]provider.Model,
) []modelInventoryModel {
	out := make([]modelInventoryModel, 0, len(ids))
	for _, modelID := range ids {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		model, ok := catalogModelLookup(catalog, modelID)
		if !ok {
			// Honesty: do not advertise tool_call without catalog evidence.
			// Reasoning is only claimed when a wire family can expose effort.
			// Unknown live ids stay conservative rather than "all capabilities".
			claimsReasoning := effortWireFamily(providerID, modelID) != ""
			model = provider.Model{
				ID: modelID, Name: modelID,
				Reasoning: claimsReasoning, ToolCall: false,
			}
		}
		if modelUnsupportedByTextPrompt(modelID, model) {
			continue
		}
		effort := catalogReasoningEffortSpec(providerID, modelID, model)
		displayID := providerID + "/" + modelID
		if info.Source != nil {
			displayID = info.Name + " / " + modelID
		}
		name := model.Name
		if strings.TrimSpace(name) == "" {
			name = modelID
		}
		out = append(out, modelInventoryModel{
			ID: providerID + "/" + modelID, DisplayID: displayID, Name: name, Available: available,
			Reasoning: model.Reasoning, ReasoningOptions: model.ReasoningOptions,
			ReasoningEfforts: effort.Options, DefaultReasoningEffort: effort.Default,
			ImageInput: modelSupportsImageInput(model), ToolCall: model.ToolCall,
		})
	}
	return out
}

func catalogModelLookup(catalog map[string]provider.Model, modelID string) (provider.Model, bool) {
	if catalog == nil {
		return provider.Model{}, false
	}
	if m, ok := catalog[modelID]; ok {
		return m, true
	}
	for key, m := range catalog {
		if m.ID == modelID || key == modelID {
			return m, true
		}
	}
	return provider.Model{}, false
}

func ensureDefaultModelPresent(models []modelInventoryModel, providerID string, info provider.Info, available bool, defaultModel string) []modelInventoryModel {
	defaultModel = strings.TrimSpace(defaultModel)
	if defaultModel == "" {
		return models
	}
	// Default may be bare model id or already "provider/model".
	bare := defaultModel
	if strings.Contains(defaultModel, "/") {
		if rest, ok := strings.CutPrefix(defaultModel, providerID+"/"); ok {
			bare = rest
		}
	}
	want := providerID + "/" + bare
	for _, m := range models {
		if m.ID == want || strings.TrimPrefix(m.ID, providerID+"/") == bare {
			return models
		}
	}
	// Inject configured default even if the live list omitted it (some proxies
	// return incomplete catalogs).
	extra := projectInventoryModels(providerID, info, available, []string{bare}, info.Models)
	if len(extra) == 0 {
		return models
	}
	return append(extra, models...)
}

func sortInventoryModels(models []modelInventoryModel, providerID, defaultModel string) {
	defaultModel = strings.TrimSpace(defaultModel)
	bare := defaultModel
	if defaultModel != "" && strings.Contains(defaultModel, "/") {
		if rest, ok := strings.CutPrefix(defaultModel, providerID+"/"); ok {
			bare = rest
		}
	}
	want := ""
	if bare != "" {
		want = providerID + "/" + bare
	}
	sort.SliceStable(models, func(i, j int) bool {
		if want != "" {
			iDef := models[i].ID == want || strings.TrimPrefix(models[i].ID, providerID+"/") == bare
			jDef := models[j].ID == want || strings.TrimPrefix(models[j].ID, providerID+"/") == bare
			if iDef != jDef {
				return iDef
			}
		}
		return models[i].ID < models[j].ID
	})
}
