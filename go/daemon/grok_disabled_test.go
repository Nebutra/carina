package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

func TestDisabledGrokBuildNeverRunsDiscovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "grok-called")
	fixture := "#!/bin/sh\nprintf called >> \"$GROK_DISABLED_MARKER\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "grok"), []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_AUTH_PATH", "")
	t.Setenv("GROK_DISABLED_MARKER", marker)
	t.Setenv("CARINA_PROVIDER_REFRESH", "0")
	provider.InvalidateGrokBuildDiscovery()
	t.Cleanup(provider.InvalidateGrokBuildDiscovery)

	disabled := map[string]bool{provider.GrokBuildProviderID: true}
	catalog := loadRuntimeProviderCatalog(false, disabled)
	assertGrokBuildWasNotProbed(t, marker, "startup catalog")
	info, exists := catalog[provider.GrokBuildProviderID]
	if !exists || info.Source == nil || info.Source.Action != grokBuildActionDisabled || info.Source.Reason != grokBuildDisabledReason {
		t.Fatalf("disabled Grok Build source=%+v", info.Source)
	}

	router := modelrouter.New()
	reasoner := newRouterReasonerWithCatalog(router, "default", provider.Catalog{provider.GrokBuildProviderID: info}, disabled)
	d := &Daemon{
		router: router, providerCatalog: provider.Catalog{provider.GrokBuildProviderID: info},
		disabledProviders: disabled, reasoner: reasoner, reasonerBackend: reasonerBackendRouter,
	}
	defer reasoner.Close()
	if d.reasonerReady() {
		t.Fatal("disabled Grok Build made the router reasoner ready")
	}
	assertGrokBuildWasNotProbed(t, marker, "reasoner readiness")

	result, err := d.handleModelList(json.RawMessage(`{"refresh":true}`))
	if err != nil {
		t.Fatal(err)
	}
	rows := result.(map[string]any)["providers"].([]modelInventoryProvider)
	if len(rows) != 1 {
		t.Fatalf("providers=%+v", rows)
	}
	row := rows[0]
	if row.ID != provider.GrokBuildProviderID || row.Registered || row.Available || row.SourceAction != grokBuildActionDisabled || row.SourceReason != grokBuildDisabledReason {
		t.Fatalf("disabled Grok Build inventory=%+v", row)
	}
	assertGrokBuildWasNotProbed(t, marker, "model inventory refresh")
}

func assertGrokBuildWasNotProbed(t *testing.T, marker, stage string) {
	t.Helper()
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s executed the Grok CLI: %v", stage, err)
	}
}
