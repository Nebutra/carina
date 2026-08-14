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

func TestOfflineCatalogAndRefreshNeverProbeGrokBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "grok-called")
	fixture := "#!/bin/sh\nprintf called >> \"$GROK_OFFLINE_MARKER\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "grok"), []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_AUTH_PATH", "")
	t.Setenv("GROK_OFFLINE_MARKER", marker)
	t.Setenv("CARINA_PROVIDER_REFRESH", "0")
	provider.InvalidateGrokBuildDiscovery()
	t.Cleanup(provider.InvalidateGrokBuildDiscovery)

	catalog := loadRuntimeProviderCatalog(true, nil)
	if _, exists := catalog[provider.GrokBuildProviderID]; exists {
		t.Fatal("offline catalog exposed Grok Build")
	}
	d := &Daemon{
		router:            modelrouter.New(),
		providerCatalog:   catalog,
		disabledProviders: map[string]bool{},
		offline:           true,
	}
	if _, err := d.handleModelList(json.RawMessage(`{"refresh":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("offline discovery executed the Grok CLI: %v", err)
	}
}
