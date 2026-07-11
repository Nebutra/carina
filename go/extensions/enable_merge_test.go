package extensions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEffectiveEnabledTruthTable(t *testing.T) {
	org := OrgExtensionPolicy{Disabled: []string{"org-off"}}
	proj := ProjectExtensionPolicy{Disabled: []string{"proj-off"}}
	cases := []struct {
		name        string
		user        bool
		ext         string
		safeMode    bool
		wantEnabled bool
		wantProv    string
	}{
		{"user enabled, no tier disables", true, "free", false, true, ProvenanceUser},
		{"user disabled, no tier disables", false, "free", false, false, ProvenanceUser},
		{"safe mode beats everything", true, "free", true, false, ProvenanceSafeMode},
		{"org disable beats user enable", true, "org-off", false, false, ProvenanceOrgPolicy},
		{"org disable binds even when user disabled", false, "org-off", false, false, ProvenanceOrgPolicy},
		{"project disable beats user enable", true, "proj-off", false, false, ProvenanceProjectPolicy},
		{"safe mode beats org", true, "org-off", true, false, ProvenanceSafeMode},
	}
	for _, tc := range cases {
		got, prov := EffectiveEnabled(tc.user, tc.ext, tc.safeMode, org, proj)
		if got != tc.wantEnabled || prov != tc.wantProv {
			t.Errorf("%s: got (%v,%q), want (%v,%q)", tc.name, got, prov, tc.wantEnabled, tc.wantProv)
		}
	}
	// DisableAll disables every name at org precedence.
	if got, prov := EffectiveEnabled(true, "anything", false, OrgExtensionPolicy{DisableAll: true}, ProjectExtensionPolicy{}); got || prov != ProvenanceOrgPolicy {
		t.Fatalf("DisableAll: got (%v,%q)", got, prov)
	}
	// The project tier is disable-only: it structurally cannot enable a
	// user-disabled extension (there is no enabling field to express it).
	if got, _ := EffectiveEnabled(false, "free", false, OrgExtensionPolicy{}, ProjectExtensionPolicy{Disabled: nil}); got {
		t.Fatal("project tier must never enable")
	}
}

func TestLoadOrgPolicyMissingZeroAndMalformedFailsClosed(t *testing.T) {
	if p := LoadOrgPolicy(""); p.DisableAll || len(p.Disabled) != 0 {
		t.Fatalf("empty dir must be zero policy: %+v", p)
	}
	dir := t.TempDir()
	if p := LoadOrgPolicy(dir); p.DisableAll || len(p.Disabled) != 0 {
		t.Fatalf("missing file must be zero policy: %+v", p)
	}
	if err := os.WriteFile(filepath.Join(dir, "extensions.json"), []byte(`{"disabled":["a","b"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := LoadOrgPolicy(dir); len(p.Disabled) != 2 || p.DisableAll {
		t.Fatalf("unexpected policy: %+v", p)
	}
	if err := os.WriteFile(filepath.Join(dir, "extensions.json"), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := LoadOrgPolicy(dir); !p.DisableAll {
		t.Fatal("malformed managed org file must fail closed to DisableAll")
	}
}

func TestLoadProjectPolicyFailsClosedToNoOp(t *testing.T) {
	if p := LoadProjectPolicy(""); len(p.Disabled) != 0 {
		t.Fatalf("empty root must be zero policy: %+v", p)
	}
	ws := t.TempDir()
	if p := LoadProjectPolicy(ws); len(p.Disabled) != 0 {
		t.Fatalf("missing file must be zero policy: %+v", p)
	}
	carina := filepath.Join(ws, ".carina")
	if err := os.MkdirAll(carina, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(carina, "extensions.json")
	if err := os.WriteFile(path, []byte(`{"disabled":["x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := LoadProjectPolicy(ws); len(p.Disabled) != 1 || p.Disabled[0] != "x" {
		t.Fatalf("unexpected policy: %+v", p)
	}
	// Malformed repository file is a no-op mask, never an error and never a
	// loosening (the tier can only disable).
	if err := os.WriteFile(path, []byte(`{"disabled": ["x"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := LoadProjectPolicy(ws); len(p.Disabled) != 0 {
		t.Fatalf("malformed project file must fail closed to no-op: %+v", p)
	}
	// Oversized repository file is DoS-bounded: capped read, treated as empty.
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), projectPolicyMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := LoadProjectPolicy(ws); len(p.Disabled) != 0 {
		t.Fatalf("oversized project file must be treated as empty: %+v", p)
	}
}

func TestSetOrgPolicySweepAndSetEnabledRejection(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	writeManifest(t, filepath.Join(root, "blocked"), Manifest{Name: "blocked", Version: "1.0.0", Components: []string{"skill"}, EstimatedPromptTokens: 7})
	writeManifest(t, filepath.Join(root, "free"), Manifest{Name: "free", Version: "1.0.0", Components: []string{"skill"}, EstimatedPromptTokens: 3})
	m, err := New(state, "0.6.1", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"blocked", "free"} {
		if _, err = m.Install(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
		if _, err = m.SetEnabled(name, true); err != nil {
			t.Fatal(err)
		}
	}

	// Startup reconcile: the org tier force-disables persisted enables.
	if err = m.SetOrgPolicy(OrgExtensionPolicy{Disabled: []string{"blocked"}}); err != nil {
		t.Fatal(err)
	}
	inv := m.Inventory()
	if inv.TotalPromptTokens != 3 {
		t.Fatalf("only 'free' should count tokens, got %d", inv.TotalPromptTokens)
	}
	for _, p := range inv.Plugins {
		switch p.Manifest.Name {
		case "blocked":
			if p.Enabled || p.EffectiveEnabled || p.EnableProvenance != ProvenanceOrgPolicy {
				t.Fatalf("blocked not swept: %+v", p)
			}
		case "free":
			if !p.Enabled || !p.EffectiveEnabled || p.EnableProvenance != ProvenanceUser {
				t.Fatalf("free should stay enabled: %+v", p)
			}
		}
	}

	// The sweep persisted: a fresh Marketplace over the same state agrees.
	m2, err := New(state, "0.6.1", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m2.Inventory().Plugins {
		if p.Manifest.Name == "blocked" && p.Enabled {
			t.Fatal("org sweep was not persisted")
		}
	}

	// Org tier cannot be re-enabled from below.
	if _, err = m.SetEnabled("blocked", true); !errors.Is(err, ErrOrgDisabled) {
		t.Fatalf("want ErrOrgDisabled, got %v", err)
	}
	// Disabling stays allowed, and install of an org-disabled extension stays
	// allowed (enable-policy, not fetch-trust).
	if _, err = m.SetEnabled("blocked", false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Install(filepath.Join(root, "blocked")); err != nil {
		t.Fatal(err)
	}

	// Project mask is a per-request view over the same state.
	view := m.InventoryForWorkspace(ProjectExtensionPolicy{Disabled: []string{"free"}})
	for _, p := range view.Plugins {
		if p.Manifest.Name == "free" {
			if !p.Enabled || p.EffectiveEnabled || p.EnableProvenance != ProvenanceProjectPolicy {
				t.Fatalf("project mask should disable 'free' without touching state: %+v", p)
			}
		}
	}
	if view.TotalPromptTokens != 0 {
		t.Fatalf("masked view should count no tokens, got %d", view.TotalPromptTokens)
	}
	// And the mask was never persisted.
	for _, p := range m.Inventory().Plugins {
		if p.Manifest.Name == "free" && (!p.Enabled || !p.EffectiveEnabled) {
			t.Fatalf("project mask leaked into state: %+v", p)
		}
	}
}
