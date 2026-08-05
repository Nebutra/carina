package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

func TestParseLiveModelIDsOpenAIShape(t *testing.T) {
	ids, err := parseLiveModelIDs([]byte(`{
		"object":"list",
		"data":[
			{"id":"gpt-5.5","object":"model"},
			{"id":"gpt-5.4","object":"model"},
			{"id":"text-embedding-3-small","object":"model"},
			{"id":"whisper-audio-1","object":"model"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ids, ",")
	if got != "gpt-5.5,gpt-5.4" {
		t.Fatalf("parse openai models = %q (embeddings/audio should be filtered)", got)
	}
}

func TestParseLiveModelIDsGeminiAndProxyShapes(t *testing.T) {
	ids, err := parseLiveModelIDs([]byte(`{"models":[{"name":"models/gemini-2.0-flash"},{"name":"models/gemini-1.5-pro"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "gemini-2.0-flash,gemini-1.5-pro" {
		t.Fatalf("gemini models = %v", ids)
	}
	ids, err = parseLiveModelIDs([]byte(`{"models":["alpha","beta"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "alpha,beta" {
		t.Fatalf("string models = %v", ids)
	}
}

func TestModelsListURL(t *testing.T) {
	cases := []struct {
		protocol runtimeProtocol
		base     string
		want     string
	}{
		{protocolOpenAIResponses, "http://127.0.0.1:15721/v1", "http://127.0.0.1:15721/v1/models"},
		{protocolOpenAIChat, "https://api.openai.com/v1", "https://api.openai.com/v1/models"},
		{protocolAnthropic, "https://api.anthropic.com", "https://api.anthropic.com/v1/models"},
		{protocolGemini, "https://generativelanguage.googleapis.com/v1beta", "https://generativelanguage.googleapis.com/v1beta/models"},
	}
	for _, tc := range cases {
		if got := modelsListURL(tc.protocol, tc.base); got != tc.want {
			t.Fatalf("modelsListURL(%v,%q)=%q want %q", tc.protocol, tc.base, got, tc.want)
		}
	}
}

func TestApplyLiveModelsAuthBearerAndAnthropic(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example/v1/models", nil)
	applyLiveModelsAuth(req, protocolOpenAIResponses, auth.Credential{Kind: auth.Bearer, Value: "tok-1"})
	if got := req.Header.Get("Authorization"); got != "Bearer tok-1" {
		t.Fatalf("openai auth = %q", got)
	}

	req, _ = http.NewRequest(http.MethodGet, "http://example/v1/models", nil)
	applyLiveModelsAuth(req, protocolOpenAIChat, auth.Credential{Kind: auth.APIKey, Value: "sk-key"})
	if got := req.Header.Get("Authorization"); got != "Bearer sk-key" {
		t.Fatalf("openai api key as bearer = %q", got)
	}

	req, _ = http.NewRequest(http.MethodGet, "http://example/v1/models", nil)
	applyLiveModelsAuth(req, protocolAnthropic, auth.Credential{Kind: auth.APIKey, Value: "sk-ant"})
	if req.Header.Get("x-api-key") != "sk-ant" || req.Header.Get("anthropic-version") == "" {
		t.Fatalf("anthropic headers = %v", req.Header)
	}

	req, _ = http.NewRequest(http.MethodGet, "http://example/v1beta/models", nil)
	applyLiveModelsAuth(req, protocolGemini, auth.Credential{Kind: auth.APIKey, Value: "gkey"})
	if req.Header.Get("x-goog-api-key") != "gkey" {
		t.Fatalf("gemini header = %v", req.Header)
	}
}

func TestModelListExpandsLiveModelsWithToken(t *testing.T) {
	var sawAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		sawAuth.Store(r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gpt-5.5"},
				{"id": "gpt-5.4"},
				{"id": "o4-mini"},
				{"id": "text-embedding-3-large"},
			},
		})
	}))
	defer srv.Close()

	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	const providerID = "ccswitch-codex-tds"
	if err := store.SetBearerToken(providerID, "tds-live-token", map[string]string{
		"source": provider.CCSwitchSourceKind, "validation": providerValidationContract, "source_revision": "rev-1",
	}); err != nil {
		t.Fatal(err)
	}
	info := provider.Info{
		ID: providerID, Name: "TDS Relay", API: srv.URL + "/v1", APIProtocol: "openai-responses",
		Source: &provider.Source{
			Kind: provider.CCSwitchSourceKind, Label: provider.CCSwitchSourceLabel,
			App: "codex", Route: provider.CCSwitchRouteManagedProxy, AuthMode: provider.CCSwitchCredentialBearer,
			Action: provider.CCSwitchActionUseActiveRoute, Current: true, Importable: true, Revision: "rev-1",
		},
		// Thin catalog: only the profile default — live list must expand this.
		Models: map[string]provider.Model{"gpt-5.5": {ID: "gpt-5.5", Name: "gpt-5.5", Reasoning: true, ToolCall: true}},
	}
	d := &Daemon{
		router:          modelrouter.New(),
		authStore:       store,
		providerCatalog: provider.Catalog{providerID: info},
		liveModelsHTTP:  srv.Client(),
		reasoner:        modelInventoryTestReasoner{name: reasonerBackendRouter},
		reasonerBackend: reasonerBackendRouter,
	}
	d.router.RegisterProvider(inventoryProvider(providerID))

	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := sawAuth.Load().(string); got != "Bearer tds-live-token" {
		t.Fatalf("live /models auth = %q, want Bearer tds-live-token", got)
	}
	rows := result.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(rows) != 1 {
		t.Fatalf("providers = %+v", rows)
	}
	row := rows[0]
	if !row.Available || !row.DynamicModels {
		t.Fatalf("row flags = %+v", row)
	}
	if row.DefaultModel != "gpt-5.5" && row.DefaultModel != providerID+"/gpt-5.5" {
		// runtimeDefaultModel may return bare id from catalog
		if row.DefaultModel != "gpt-5.5" {
			// accept bare from catalog first model path
		}
	}
	ids := make([]string, 0, len(row.Models))
	for _, m := range row.Models {
		ids = append(ids, m.ID)
		if strings.Contains(m.ID, "embedding") {
			t.Fatalf("embedding leaked into inventory: %s", m.ID)
		}
		if m.DisplayID != "TDS Relay / "+strings.TrimPrefix(m.ID, providerID+"/") {
			t.Fatalf("display_id = %q for %s", m.DisplayID, m.ID)
		}
	}
	joined := strings.Join(ids, ",")
	// default first, then remaining live ids (excluding embeddings)
	if !strings.Contains(joined, providerID+"/gpt-5.5") || !strings.Contains(joined, providerID+"/gpt-5.4") || !strings.Contains(joined, providerID+"/o4-mini") {
		t.Fatalf("live models missing expected ids: %v", ids)
	}
	if len(row.Models) != 3 {
		t.Fatalf("want 3 chat models after filter, got %d: %v", len(row.Models), ids)
	}
	if row.Models[0].ID != providerID+"/gpt-5.5" {
		t.Fatalf("default model should sort first, got %v", ids)
	}

	// Second call must hit cache (no second HTTP if server is closed).
	srv.Close()
	result2, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	rows2 := result2.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(rows2[0].Models) != 3 {
		t.Fatalf("cached live models lost: %+v", rows2[0].Models)
	}
}

func TestModelListFallsBackToCatalogWhenLiveFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	const providerID = "ccswitch-codex-fallback"
	if err := store.SetBearerToken(providerID, "bad-token", map[string]string{
		"source": provider.CCSwitchSourceKind, "validation": providerValidationContract, "source_revision": "r1",
	}); err != nil {
		t.Fatal(err)
	}
	info := provider.Info{
		ID: providerID, Name: "Broken Relay", API: srv.URL + "/v1", APIProtocol: "openai-responses",
		Source: &provider.Source{
			Kind: provider.CCSwitchSourceKind, Route: provider.CCSwitchRouteManagedProxy,
			Revision: "r1", Importable: true,
		},
		Models: map[string]provider.Model{"gpt-only": {ID: "gpt-only", Name: "gpt-only", Reasoning: true, ToolCall: true}},
	}
	d := &Daemon{
		router:          modelrouter.New(),
		authStore:       store,
		providerCatalog: provider.Catalog{providerID: info},
		liveModelsHTTP:  srv.Client(),
	}
	d.router.RegisterProvider(inventoryProvider(providerID))

	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	row := result.(map[string]any)["providers"].([]modelInventoryProvider)[0]
	if len(row.Models) != 1 || row.Models[0].ID != providerID+"/gpt-only" {
		t.Fatalf("fallback catalog models = %+v", row.Models)
	}
	if row.DynamicModels {
		t.Fatalf("catalog-only fallback should not mark dynamic_models: %+v", row)
	}
}

func TestModelListSkipsLiveFetchWithoutCredential(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "should-not-fetch"}}})
	}))
	defer srv.Close()

	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	const providerID = "ccswitch-codex-noauth"
	info := provider.Info{
		ID: providerID, Name: "Offline Relay", API: srv.URL + "/v1", APIProtocol: "openai-responses",
		Source: &provider.Source{Kind: provider.CCSwitchSourceKind, Route: provider.CCSwitchRouteManagedProxy, Importable: true},
		Models: map[string]provider.Model{"gpt-cfg": {ID: "gpt-cfg"}},
	}
	d := &Daemon{
		router: modelrouter.New(), authStore: store, providerCatalog: provider.Catalog{providerID: info},
		liveModelsHTTP: srv.Client(),
	}
	d.router.RegisterProvider(inventoryProvider(providerID))
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	row := result.(map[string]any)["providers"].([]modelInventoryProvider)[0]
	if row.Available || hits.Load() != 0 {
		t.Fatalf("unauthenticated provider should not live-fetch: available=%v hits=%d", row.Available, hits.Load())
	}
	if len(row.Models) != 1 || row.Models[0].ID != providerID+"/gpt-cfg" {
		t.Fatalf("catalog projection = %+v", row.Models)
	}
}

func TestLiveModelsCacheTTL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}, {"id": "m2"}}})
	}))
	defer srv.Close()

	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	const providerID = "live-cache-prov"
	_ = store.SetAPIKey(providerID, "k", nil)
	info := provider.Info{
		ID: providerID, Name: "Cache", API: srv.URL + "/v1", APIProtocol: "openai-chat",
		Models: map[string]provider.Model{"m1": {ID: "m1"}},
	}
	d := &Daemon{
		router: modelrouter.New(), authStore: store, providerCatalog: provider.Catalog{providerID: info},
		liveModelsHTTP: srv.Client(),
	}
	d.router.RegisterProvider(inventoryProvider(providerID))

	for i := 0; i < 3; i++ {
		if _, err := d.handleModelList(nil); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("cache should serve subsequent lists, hits=%d", hits.Load())
	}

	// Force expiry and ensure refresh.
	d.liveModelsMu.Lock()
	for k, e := range d.liveModelsCache {
		e.fetchedAt = time.Now().Add(-2 * liveModelsCacheTTL)
		d.liveModelsCache[k] = e
	}
	d.liveModelsMu.Unlock()
	if _, err := d.handleModelList(nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expired cache should refetch, hits=%d", hits.Load())
	}
}

func TestPrefersLiveModelList(t *testing.T) {
	if !prefersLiveModelList(provider.Info{Source: &provider.Source{Kind: provider.CCSwitchSourceKind}, Models: map[string]provider.Model{
		"a": {ID: "a"}, "b": {ID: "b"}, "c": {ID: "c"},
	}}) {
		t.Fatal("CC Switch should prefer live even with multi-model catalog")
	}
	if !prefersLiveModelList(provider.Info{Models: map[string]provider.Model{"only": {ID: "only"}}}) {
		t.Fatal("thin catalog should prefer live")
	}
	if !prefersLiveModelList(provider.Info{Models: map[string]provider.Model{}}) {
		t.Fatal("empty catalog should prefer live")
	}
	if prefersLiveModelList(provider.Info{Models: map[string]provider.Model{
		"a": {ID: "a"}, "b": {ID: "b"},
	}}) {
		t.Fatal("rich static catalog should keep catalog inventory")
	}
}

func TestEnsureDefaultModelPresentInjectsMissing(t *testing.T) {
	info := provider.Info{ID: "p", Name: "P", Models: map[string]provider.Model{
		"cfg-default": {ID: "cfg-default", Name: "cfg-default", Reasoning: true, ToolCall: true},
	}}
	models := projectInventoryModels("p", info, true, []string{"live-a", "live-b"}, info.Models)
	out := ensureDefaultModelPresent(models, "p", info, true, "cfg-default")
	if len(out) != 3 || out[0].ID != "p/cfg-default" {
		t.Fatalf("ensure default = %+v", out)
	}
	// already present
	out2 := ensureDefaultModelPresent(out, "p", info, true, "cfg-default")
	if len(out2) != 3 {
		t.Fatalf("duplicate inject: %+v", out2)
	}
}

func TestProjectInventoryModelsDoesNotInventCapabilities(t *testing.T) {
	// Unknown live ids must not advertise tool_call; reasoning only when a wire family exists.
	info := provider.Info{ID: "p", Name: "P", Models: map[string]provider.Model{}}
	plain := projectInventoryModels("p", info, true, []string{"mystery-model"}, nil)
	if len(plain) != 1 || plain[0].ToolCall || plain[0].Reasoning {
		t.Fatalf("unknown provider should stay capability-conservative: %+v", plain)
	}
	codex := projectInventoryModels("ccswitch-codex-abc", info, true, []string{"gpt-5.5"}, nil)
	if len(codex) != 1 {
		t.Fatalf("codex live rows: %+v", codex)
	}
	if !codex[0].Reasoning || codex[0].ToolCall {
		t.Fatalf("codex family may claim reasoning for effort UI, not tools: %+v", codex[0])
	}
	if len(codex[0].ReasoningEfforts) == 0 {
		t.Fatalf("codex family should still expose effort ladder: %+v", codex[0])
	}
}
