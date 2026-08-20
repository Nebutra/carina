package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectTUIThemeLayersAndKeepsOwningPath(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	globalPath := filepath.Join(home, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, []byte(`{"tui_theme":" Dark "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pref, err := InspectTUITheme(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if pref.Value != "dark" || pref.PersistPath != globalPath {
		t.Fatalf("global theme = %#v", pref)
	}

	projectPath := filepath.Join(project, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(`{"tui_theme":"LIGHT"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pref, err = InspectTUITheme(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if pref.Value != "light" || pref.PersistPath != projectPath {
		t.Fatalf("project theme = %#v", pref)
	}
}

func TestInspectTUIThemeDefaultsAndAcceptsEveryPolarity(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	pref, err := InspectTUITheme(home, project)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(home, ".carina", "config.json")
	if pref.Value != defaultTUITheme || pref.PersistPath != wantPath {
		t.Fatalf("default theme = %#v", pref)
	}

	path := filepath.Join(home, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"auto", "dark", "light"} {
		t.Run(value, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(`{"tui_theme":"`+value+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := InspectTUITheme(home, project)
			if err != nil {
				t.Fatal(err)
			}
			if got.Value != value {
				t.Fatalf("theme = %q, want %q", got.Value, value)
			}
		})
	}
}

func TestInspectTUIThemeRejectsInvalidAndDuplicateValues(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	globalPath := filepath.Join(home, ".carina", "config.json")
	projectPath := filepath.Join(project, ".carina", "config.json")
	for _, path := range []string{globalPath, projectPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name string
		path string
		json string
	}{
		{name: "invalid global despite valid project", path: globalPath, json: `{"tui_theme":"tokyo"}`},
		{name: "invalid project", path: projectPath, json: `{"tui_theme":"night"}`},
		{name: "wrong type", path: projectPath, json: `{"tui_theme":true}`},
		{name: "duplicate", path: projectPath, json: `{"tui_theme":"auto","tui_theme":"dark"}`},
	}
	if err := os.WriteFile(projectPath, []byte(`{"tui_theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(tt.path, []byte(tt.json), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectTUITheme(home, project); err == nil || !strings.Contains(err.Error(), "tui_theme") {
				t.Fatalf("InspectTUITheme() error = %v, want field-specific failure", err)
			}
			if tt.path == globalPath {
				if err := os.WriteFile(globalPath, []byte(`{"tui_theme":"auto"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(projectPath, []byte(`{"tui_theme":"dark"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigValidatesTUITheme(t *testing.T) {
	for _, value := range []string{"auto", "dark", "light", " LIGHT "} {
		cfg := Defaults(t.TempDir())
		cfg.TUITheme = value
		if err := cfg.Validate(); err != nil {
			t.Fatalf("TUITheme %q rejected: %v", value, err)
		}
	}

	cfg := Defaults(t.TempDir())
	cfg.TUITheme = "tokyo"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tui_theme") {
		t.Fatalf("invalid TUITheme error = %v", err)
	}
}
