package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectTUIGlyphsLayersAndKeepsOwningPath(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	globalPath := filepath.Join(home, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, []byte(`{"tui_glyphs":" unicode "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pref, err := InspectTUIGlyphs(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if pref.Value != "unicode" || pref.PersistPath != globalPath {
		t.Fatalf("global glyphs = %#v", pref)
	}

	projectPath := filepath.Join(project, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(`{"tui_glyphs":"NERD"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pref, err = InspectTUIGlyphs(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if pref.Value != "nerd" || pref.PersistPath != projectPath {
		t.Fatalf("project glyphs = %#v", pref)
	}
}

func TestInspectTUIGlyphsDefaultsAndAcceptsEveryTier(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	pref, err := InspectTUIGlyphs(home, project)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(home, ".carina", "config.json")
	if pref.Value != defaultTUIGlyphs || pref.PersistPath != wantPath {
		t.Fatalf("default glyphs = %#v", pref)
	}

	path := filepath.Join(home, ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"auto", "unicode", "nerd", "ascii"} {
		t.Run(value, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(`{"tui_glyphs":"`+value+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := InspectTUIGlyphs(home, project)
			if err != nil {
				t.Fatal(err)
			}
			if got.Value != value {
				t.Fatalf("glyphs = %q, want %q", got.Value, value)
			}
		})
	}
}

func TestInspectTUIGlyphsRejectsInvalidAndDuplicateValues(t *testing.T) {
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
		{name: "invalid global despite valid project", path: globalPath, json: `{"tui_glyphs":"icons"}`},
		{name: "invalid project", path: projectPath, json: `{"tui_glyphs":"emoji"}`},
		{name: "wrong type", path: projectPath, json: `{"tui_glyphs":true}`},
		{name: "duplicate", path: projectPath, json: `{"tui_glyphs":"auto","tui_glyphs":"ascii"}`},
	}
	if err := os.WriteFile(projectPath, []byte(`{"tui_glyphs":"ascii"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(tt.path, []byte(tt.json), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectTUIGlyphs(home, project); err == nil || !strings.Contains(err.Error(), "tui_glyphs") {
				t.Fatalf("InspectTUIGlyphs() error = %v, want field-specific failure", err)
			}
			if tt.path == globalPath {
				if err := os.WriteFile(globalPath, []byte(`{"tui_glyphs":"auto"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(projectPath, []byte(`{"tui_glyphs":"ascii"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigValidatesTUIGlyphs(t *testing.T) {
	for _, value := range []string{"auto", "unicode", "nerd", "ascii", " NERD "} {
		cfg := Defaults(t.TempDir())
		cfg.TUIGlyphs = value
		if err := cfg.Validate(); err != nil {
			t.Fatalf("TUIGlyphs %q rejected: %v", value, err)
		}
	}

	cfg := Defaults(t.TempDir())
	cfg.TUIGlyphs = "icons"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tui_glyphs") {
		t.Fatalf("invalid TUIGlyphs error = %v", err)
	}
}
