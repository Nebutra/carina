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
