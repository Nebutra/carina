package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectTUIDensityLayersAndKeepsOwningPath(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	global := filepath.Join(home, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"tui_density":"comfortable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pref, err := InspectTUIDensity(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if pref.Value != "comfortable" || pref.PersistPath != global {
		t.Fatalf("global density = %#v", pref)
	}

	projectPath := filepath.Join(project, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(`{"tui_density":"compact"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pref, err = InspectTUIDensity(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if pref.Value != "compact" || pref.PersistPath != projectPath {
		t.Fatalf("project density = %#v", pref)
	}
}

func TestInspectTUIDensityDefaultsAndRejectsInvalidValues(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	pref, err := InspectTUIDensity(home, project)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(home, ".carina", "config.json")
	if pref.Value != "compact" || pref.PersistPath != wantPath {
		t.Fatalf("default density = %#v", pref)
	}

	path := filepath.Join(home, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"tui_density":"dense"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectTUIDensity(home, project); err == nil {
		t.Fatal("invalid density must fail")
	}

	if err := os.WriteFile(path, []byte(`{"tui_density":"compact","tui_density":"comfortable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectTUIDensity(home, project); err == nil {
		t.Fatal("duplicate density keys must fail")
	}
}
