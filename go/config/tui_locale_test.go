package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectAndUpdateTUILocalePreservesUnrelatedConfig(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	global := filepath.Join(home, ".carina", "config.json")
	projectConfig := filepath.Join(project, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(global), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"max_concurrent_tasks":4,"tui_locale":"en"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectConfig, []byte(`{"tui_locale":"not-real","sandbox_commands":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	pref, err := InspectTUILocale(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if pref.Value != "not-real" || pref.Source != "project" || pref.PersistPath != projectConfig || pref.Valid {
		t.Fatalf("preference = %+v", pref)
	}
	if err := UpdateTUILocale(pref.PersistPath, "zh-Hant"); err != nil {
		t.Fatal(err)
	}
	pref, err = InspectTUILocale(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if !pref.Valid || pref.Canonical != "zh-Hant" {
		t.Fatalf("repaired preference = %+v", pref)
	}
	raw, err := os.ReadFile(projectConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(raw), `"sandbox_commands": true`, `"tui_locale": "zh-Hant"`) {
		t.Fatalf("locale update lost unrelated config:\n%s", raw)
	}
}

func TestInspectTUILocaleDefaultsPersistenceToGlobalConfig(t *testing.T) {
	home := t.TempDir()
	pref, err := InspectTUILocale(home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if pref.Value != "" || pref.PersistPath != filepath.Join(home, ".carina", "config.json") {
		t.Fatalf("preference = %+v", pref)
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
