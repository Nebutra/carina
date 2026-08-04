package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

type modelInventoryTestReasoner struct{ name string }

func (r modelInventoryTestReasoner) Name() string                                { return r.name }
func (modelInventoryTestReasoner) Think(context.Context, string) (string, error) { return "", nil }

type inventoryProvider string

func (p inventoryProvider) Name() string { return string(p) }
func (inventoryProvider) Complete(context.Context, modelrouter.Request) (*modelrouter.Response, error) {
	return nil, nil
}

func TestModelListReportsAvailabilityWithoutSecrets(t *testing.T) {
	secret := "inventory-secret-must-not-leak"
	t.Setenv("OPENAI_API_KEY", secret)
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.router.RegisterProvider(inventoryProvider("openai"))
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), secret) {
		t.Fatal("model inventory leaked credential value")
	}
	providers := result.(map[string]any)["providers"].([]modelInventoryProvider)
	found := false
	for _, provider := range providers {
		if provider.ID != "openai" {
			continue
		}
		found = true
		if !provider.Registered || !provider.Available || provider.AuthSource != "env:OPENAI_API_KEY" {
			t.Fatalf("openai availability = %+v", provider)
		}
		if len(provider.Models) == 0 || !strings.HasPrefix(provider.Models[0].ID, "openai/") {
			t.Fatalf("openai models = %+v", provider.Models)
		}
	}
	if !found {
		t.Fatalf("openai missing from inventory: %+v", providers)
	}
}

func TestModelListReportsConcreteDefaultAndDaemonOwnedReasoner(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "inventory-key")
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.router.RegisterProvider(inventoryProvider("openai"))
	d.reasoner = modelInventoryTestReasoner{name: reasonerBackendRouter}
	d.reasonerBackend = reasonerBackendRouter

	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	response := result.(map[string]any)
	defaultModel, _ := response["default_model"].(string)
	if defaultModel == "" || defaultModel == "default" || !strings.HasPrefix(defaultModel, "openai/") {
		t.Fatalf("default_model = %q, want concrete openai model", defaultModel)
	}
	reasoner := response["reasoner"].(modelInventoryReasoner)
	if reasoner.Backend != reasonerBackendRouter || !reasoner.Available || reasoner.Explicit {
		t.Fatalf("reasoner = %+v", reasoner)
	}
}

func TestModelListBecomesExecutionReadyAfterCredentialWriteWithoutDaemonRestart(t *testing.T) {
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	cat := provider.Catalog{"openai": {
		ID: "openai", API: "https://api.openai.com/v1", NPM: "@ai-sdk/openai",
		Models: map[string]provider.Model{"gpt-test": {ID: "gpt-test"}},
	}}
	d := &Daemon{
		router:            modelrouter.New(),
		authStore:         store,
		providerCatalog:   cat,
		disabledProviders: map[string]bool{},
		reasoner:          modelInventoryTestReasoner{name: reasonerBackendRouter},
		reasonerBackend:   reasonerBackendRouter,
	}
	d.router.RegisterProvider(inventoryProvider("openai"))

	readiness := func() (bool, bool) {
		result, listErr := d.handleModelList(nil)
		if listErr != nil {
			t.Fatal(listErr)
		}
		response := result.(map[string]any)
		providers := response["providers"].([]modelInventoryProvider)
		return providers[0].Available, response["reasoner"].(modelInventoryReasoner).Available
	}
	if providerReady, executionReady := readiness(); providerReady || executionReady {
		t.Fatalf("readiness before import = provider:%v execution:%v", providerReady, executionReady)
	}
	if err := store.SetAPIKey("openai", "runtime-imported-key", nil); err != nil {
		t.Fatal(err)
	}
	if providerReady, executionReady := readiness(); !providerReady || !executionReady {
		t.Fatalf("readiness after import = provider:%v execution:%v", providerReady, executionReady)
	}
}

func TestModelListReportsOnlyExplicitlyConstructedCLIReasoner(t *testing.T) {
	d := &Daemon{router: modelrouter.New()}
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	if reasoner := result.(map[string]any)["reasoner"].(modelInventoryReasoner); reasoner.Available || reasoner.Backend != "" {
		t.Fatalf("binary presence must not create reasoner inventory: %+v", reasoner)
	}

	d.reasoner = modelInventoryTestReasoner{name: reasonerBackendCodexCLI}
	d.reasonerBackend = reasonerBackendCodexCLI
	d.reasonerExplicit = true
	result, err = d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	reasoner := result.(map[string]any)["reasoner"].(modelInventoryReasoner)
	if reasoner.Backend != reasonerBackendCodexCLI || !reasoner.Available || !reasoner.Explicit {
		t.Fatalf("explicit CLI reasoner = %+v", reasoner)
	}
}

func TestModelListRequiresExplicitKeylessLocalEndpoint(t *testing.T) {
	t.Setenv("LMSTUDIO_BASE_URL", "")
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		router:    modelrouter.New(),
		authStore: store,
		providerCatalog: provider.Catalog{
			"lmstudio": {
				ID: "lmstudio", API: "http://127.0.0.1:1234/v1", NPM: "@ai-sdk/openai-compatible",
			},
		},
	}
	d.router.RegisterProvider(inventoryProvider("lmstudio"))

	availability := func() bool {
		result, err := d.handleModelList(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range result.(map[string]any)["providers"].([]modelInventoryProvider) {
			if row.ID == "lmstudio" {
				return row.Available
			}
		}
		t.Fatal("lmstudio missing from inventory")
		return false
	}
	if availability() {
		t.Fatal("catalog-default localhost endpoint must not be reported available")
	}
	t.Setenv("LMSTUDIO_BASE_URL", "http://127.0.0.1:1234/v1")
	if !availability() {
		t.Fatal("explicit keyless localhost endpoint should be reported available")
	}
}

func TestModelListPublishesInputAndToolCapabilities(t *testing.T) {
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.SetAPIKey("vision", "secret", nil); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		router:    modelrouter.New(),
		authStore: store,
		providerCatalog: provider.Catalog{"vision": {
			ID: "vision", API: "https://example.test/v1", Env: []string{"VISION_API_KEY"}, NPM: "@ai-sdk/openai-compatible",
			Models: map[string]provider.Model{"see": {
				ID: "see", ToolCall: true, Modalities: &provider.Modalities{Input: []string{"text", "image"}, Output: []string{"text"}},
			}},
		}},
	}
	d.router.RegisterProvider(inventoryProvider("vision"))
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	providers := result.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(providers) != 1 || len(providers[0].Models) != 1 || !providers[0].Models[0].ImageInput || !providers[0].Models[0].ToolCall {
		t.Fatalf("model capabilities = %+v", providers)
	}
}

func TestModelListProjectsCCSwitchDiscoveryWithoutSourceIDsOrSecrets(t *testing.T) {
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	d := &Daemon{
		router:    modelrouter.New(),
		authStore: store,
		providerCatalog: provider.Catalog{"ccswitch-codex-safe": {
			ID: "ccswitch-codex-safe", Name: "Relay", API: "https://relay.example/v1", APIProtocol: "openai-responses",
			Source: &provider.Source{
				Kind: provider.CCSwitchSourceKind, Label: provider.CCSwitchSourceLabel,
				App: "codex", Route: provider.CCSwitchRouteManagedProxy, AuthMode: provider.CCSwitchCredentialBearer,
				Action: provider.CCSwitchActionUseActiveRoute, Current: true, Importable: true,
			},
			Models: map[string]provider.Model{"gpt-test": {ID: "gpt-test"}},
		}},
	}
	d.router.RegisterProvider(inventoryProvider("ccswitch-codex-safe"))
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	providers := result.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(providers) != 1 {
		t.Fatalf("providers = %+v", providers)
	}
	row := providers[0]
	if row.Available || row.SourceKind != provider.CCSwitchSourceKind || row.SourceLabel != "CC Switch" || !row.SourceCurrent || !row.SourceImportable {
		t.Fatalf("discovered row = %+v", row)
	}
	if row.SourceApp != "codex" || row.SourceRoute != provider.CCSwitchRouteManagedProxy || row.SourceAuthMode != provider.CCSwitchCredentialBearer || row.SourceAction != provider.CCSwitchActionUseActiveRoute {
		t.Fatalf("typed source route = %+v", row)
	}
	if len(row.Models) != 1 || row.Models[0].DisplayID != "Relay / gpt-test" {
		t.Fatalf("safe model display projection = %+v", row.Models)
	}
	if got := detectRuntimeProtocol(d.providerCatalog[row.ID]); got != protocolOpenAIResponses {
		t.Fatalf("runtime protocol = %q", got)
	}
}

func TestModelListRejectsExplainUnavailableCCSwitchEvenWithStoredCredential(t *testing.T) {
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	const providerID = "ccswitch-codex-managed-proxy"
	if err := store.SetBearerToken(providerID, "stale-managed-token", map[string]string{
		"source": provider.CCSwitchSourceKind, "validation": providerValidationContract, "source_revision": "rev-1",
	}); err != nil {
		t.Fatal(err)
	}
	info := provider.Info{
		ID: providerID, Name: "Mox", API: "http://127.0.0.1:15721/v1", APIProtocol: "openai-responses",
		Source: &provider.Source{
			Kind: provider.CCSwitchSourceKind, Label: provider.CCSwitchSourceLabel, App: "codex",
			Route: provider.CCSwitchRouteManagedProxy, AuthMode: provider.CCSwitchCredentialCLIOAuth,
			Action: provider.CCSwitchActionExplainUnavailable, Current: true, Importable: false,
			Revision: "rev-1", Reason: "The active CC Switch Codex route has no reusable proxy access token",
		},
		// Empty catalog mirrors non-importable managed proxy projection.
		Models: map[string]provider.Model{},
	}
	d := &Daemon{
		router: modelrouter.New(), authStore: store, providerCatalog: provider.Catalog{providerID: info},
		reasoner: modelInventoryTestReasoner{name: reasonerBackendRouter}, reasonerBackend: reasonerBackendRouter,
	}
	d.router.RegisterProvider(inventoryProvider(providerID))
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := result.(map[string]any)
	rows := payload["providers"].([]modelInventoryProvider)
	if len(rows) != 1 {
		t.Fatalf("providers = %+v", rows)
	}
	row := rows[0]
	if row.Available || row.SourceAction != provider.CCSwitchActionExplainUnavailable {
		t.Fatalf("explain_unavailable managed proxy became runnable: %+v", row)
	}
	if len(row.Models) != 0 {
		t.Fatalf("non-importable route should keep empty model inventory: %+v", row.Models)
	}
	if row.DefaultModel != "" {
		t.Fatalf("placeholder default must not surface: %q", row.DefaultModel)
	}
	if defaultModel, _ := payload["default_model"].(string); defaultModel != "" {
		t.Fatalf("inventory default_model = %q, want empty", defaultModel)
	}
	reasoner := payload["reasoner"].(modelInventoryReasoner)
	if reasoner.Available {
		t.Fatalf("router reasoner must not be available without a real model: %+v", reasoner)
	}
	if d.reasonerReady() {
		t.Fatal("reasonerReady should ignore non-executable CC Switch sources")
	}
}

func TestModelListRequiresMatchingCCSwitchSourceRevision(t *testing.T) {
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	const providerID = "ccswitch-codex-managed-proxy"
	if err := store.SetBearerToken(providerID, "stale-secret", map[string]string{
		"source": provider.CCSwitchSourceKind, "validation": providerValidationContract, "source_revision": "old",
	}); err != nil {
		t.Fatal(err)
	}
	info := provider.Info{
		ID: providerID, Name: "Active Codex route", API: "http://127.0.0.1:15721/v1", APIProtocol: "openai-responses",
		Source: &provider.Source{
			Kind: provider.CCSwitchSourceKind, Route: provider.CCSwitchRouteManagedProxy,
			Revision: "current", Importable: true,
		},
		Models: map[string]provider.Model{"gpt-live": {ID: "gpt-live"}},
	}
	d := &Daemon{
		router: modelrouter.New(), authStore: store, providerCatalog: provider.Catalog{providerID: info},
		reasoner: modelInventoryTestReasoner{name: reasonerBackendRouter}, reasonerBackend: reasonerBackendRouter,
	}
	d.router.RegisterProvider(inventoryProvider(providerID))
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := result.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(rows) != 1 || rows[0].Available || rows[0].AuthSource != "" || d.reasonerReady() {
		t.Fatalf("stale managed credential became runnable: %+v", rows)
	}
	if err := store.SetBearerToken(providerID, "current-secret", map[string]string{
		"source": provider.CCSwitchSourceKind, "validation": providerValidationContract, "source_revision": "current",
	}); err != nil {
		t.Fatal(err)
	}
	result, err = d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	rows = result.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(rows) != 1 || !rows[0].Available || rows[0].AuthSource != "auth:"+providerID || !d.reasonerReady() {
		t.Fatalf("current managed credential not runnable: %+v", rows)
	}
}

func TestModelListOrdersRunnableThenManagedThenSavedThenOrdinary(t *testing.T) {
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.SetAPIKey("ready", "secret", nil); err != nil {
		t.Fatal(err)
	}
	catalog := provider.Catalog{
		"ordinary": {ID: "ordinary", Name: "A ordinary", API: "https://ordinary.example/v1", APIProtocol: "openai-responses"},
		"saved": {
			ID: "saved", Name: "B saved", API: "https://saved.example/v1", APIProtocol: "openai-responses",
			Source: &provider.Source{Kind: provider.CCSwitchSourceKind, Route: provider.CCSwitchRouteSavedProfile, Importable: true},
		},
		"managed": {
			ID: "managed", Name: "Z managed", API: "http://127.0.0.1:15721/v1", APIProtocol: "openai-responses",
			Source: &provider.Source{Kind: provider.CCSwitchSourceKind, Route: provider.CCSwitchRouteManagedProxy, Importable: true},
		},
		"ready": {ID: "ready", Name: "Z ready", API: "https://ready.example/v1", APIProtocol: "openai-responses"},
	}
	d := &Daemon{router: modelrouter.New(), authStore: store, providerCatalog: catalog}
	for id := range catalog {
		d.router.RegisterProvider(inventoryProvider(id))
	}
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := result.(map[string]any)["providers"].([]modelInventoryProvider)
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.ID)
	}
	want := []string{"ready", "managed", "saved", "ordinary"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("provider order = %v, want %v", got, want)
	}
}

func TestModelListRequiresCurrentCCSwitchValidationContract(t *testing.T) {
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	const providerID = "ccswitch-claude-safe"
	if err := store.SetAPIKey(providerID, "legacy-secret", map[string]string{"source": provider.CCSwitchSourceKind}); err != nil {
		t.Fatal(err)
	}
	info := provider.Info{
		ID: providerID, Name: "Claude Relay", API: "https://relay.example", APIProtocol: "anthropic",
		Source: &provider.Source{Kind: provider.CCSwitchSourceKind, Label: provider.CCSwitchSourceLabel, Importable: true},
		Models: map[string]provider.Model{"claude-test": {ID: "claude-test"}},
	}
	d := &Daemon{router: modelrouter.New(), authStore: store, providerCatalog: provider.Catalog{providerID: info}}
	d.router.RegisterProvider(inventoryProvider(providerID))
	result, err := d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	providers := result.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(providers) != 1 || providers[0].Available || providers[0].AuthSource != "" {
		t.Fatalf("legacy unvalidated CC Switch credential became runnable: %+v", providers)
	}
	if err := store.SetBearerToken(providerID, "validated-secret", map[string]string{
		"source": provider.CCSwitchSourceKind, "validation": providerValidationContract,
	}); err != nil {
		t.Fatal(err)
	}
	result, err = d.handleModelList(nil)
	if err != nil {
		t.Fatal(err)
	}
	providers = result.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(providers) != 1 || !providers[0].Available || providers[0].AuthSource != "auth:"+providerID {
		t.Fatalf("validated CC Switch credential not runnable: %+v", providers)
	}
}
