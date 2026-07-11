package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir string, m Manifest) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "carina-extension.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestTrustedInstallDependenciesTokensAndSafeMode(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	writeManifest(t, filepath.Join(root, "base"), Manifest{Name: "base", Version: "1.2.0", Components: []string{"skill"}, RuntimeConstraint: ">=0.6.0", EstimatedPromptTokens: 20})
	m, err := New(state, "0.6.1", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Install(filepath.Join(root, "base")); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SetEnabled("base", true); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, filepath.Join(root, "child"), Manifest{Name: "child", Version: "1.0.0", Components: []string{"worker", "artifact-adapter"}, Dependencies: []Dependency{{Name: "base", Constraint: ">=1.0.0"}}, EstimatedPromptTokens: 5})
	if _, err = m.Install(filepath.Join(root, "child")); err != nil {
		t.Fatal(err)
	}
	if inv := m.Inventory(); inv.TotalPromptTokens != 20 || len(inv.Plugins) != 2 {
		t.Fatalf("%+v", inv)
	}
	if err = m.SetSafeMode(true); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SetEnabled("base", true); err == nil {
		t.Fatal("safe mode should fail closed")
	}
}
func TestRejectsUntrustedAndUnsupported(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	m, _ := New(t.TempDir(), "0.6.1", []string{root})
	writeManifest(t, other, Manifest{Name: "x", Version: "1.0.0"})
	if _, err := m.Install(other); err == nil {
		t.Fatal("untrusted source accepted")
	}
	dir := filepath.Join(root, "bad")
	writeManifest(t, dir, Manifest{Name: "bad", Version: "1.0.0", Components: []string{"native-exec"}})
	if _, err := m.Install(dir); err == nil {
		t.Fatal("native execution accepted")
	}
}
