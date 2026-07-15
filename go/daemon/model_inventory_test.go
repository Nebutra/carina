package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	modelrouter "github.com/Nebutra/carina/go/model-router"
)

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
