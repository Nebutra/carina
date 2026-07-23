package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResolvedTracksSocketAndStateSources(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	project := t.TempDir()
	writeConfig(t, home, `{"socket":"/global.sock","state_dir":"/global-state"}`)
	writeConfig(t, project, `{"socket":"/project.sock"}`)
	t.Setenv("CARINA_STATE_DIR", "/env-state")

	resolved, err := LoadResolvedWithManaged(home, project, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provenance.KeySources["socket"] != "project" {
		t.Fatalf("socket source = %q", resolved.Provenance.KeySources["socket"])
	}
	if resolved.Provenance.KeySources["state_dir"] != "environment" {
		t.Fatalf("state source = %q", resolved.Provenance.KeySources["state_dir"])
	}
	if resolved.Fingerprint == "" {
		t.Fatal("missing fingerprint")
	}
}

func TestLoadResolvedLockedSourceWins(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	managed := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(managed, []byte(`{"values":{"socket":"/managed.sock"},"locked_keys":["socket"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARINA_SOCKET", "/env.sock")
	resolved, err := LoadResolvedWithManaged(home, "", managed)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Config.Socket != "/managed.sock" || resolved.Provenance.KeySources["socket"] != "managed_locked" {
		t.Fatalf("resolved = %+v provenance=%+v", resolved.Config, resolved.Provenance)
	}
}

func TestFingerprintChangesWithEffectiveConfig(t *testing.T) {
	one := Defaults(t.TempDir())
	two := one
	two.Offline = true
	a, err := Fingerprint(one)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fingerprint(two)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("effective config change did not change fingerprint")
	}
}
