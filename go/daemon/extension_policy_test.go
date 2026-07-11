package daemon_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/extensions"
	"github.com/Nebutra/carina/go/rpc"
)

type extensionInventory struct {
	Plugins []struct {
		Manifest struct {
			Name string `json:"name"`
		} `json:"manifest"`
		Enabled          bool   `json:"enabled"`
		EffectiveEnabled bool   `json:"effective_enabled"`
		EnableProvenance string `json:"enable_provenance"`
	} `json:"plugins"`
	SafeMode          bool `json:"safe_mode"`
	TotalPromptTokens int  `json:"total_prompt_tokens"`
}

func writeExtensionManifest(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(extensions.Manifest{Name: name, Version: "1.0.0", Components: []string{"skill"}})
	if err := os.WriteFile(filepath.Join(dir, "carina-extension.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestExtensionOrgPolicyReconcileAndProjectMask verifies the tri-level
// enable-merge end to end: PolicyDir wiring, the startup force-disable
// reconcile of persisted enables, SetEnabled rejection for org-disabled
// extensions, and the per-request project mask on extension.list.
func TestExtensionOrgPolicyReconcileAndProjectMask(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}

	stateDir := t.TempDir()
	extRoot := t.TempDir()
	toolsDir := filepath.Join(repoRoot, "zig/zig-out/bin")
	writeExtensionManifest(t, filepath.Join(extRoot, "blocked"), "blocked")
	writeExtensionManifest(t, filepath.Join(extRoot, "free"), "free")

	// Run 1: no org policy. Install and enable both extensions, then stop —
	// the enabled state persists in <StateDir>/extensions.json.
	d1, err := daemon.New(daemon.Options{
		StateDir: stateDir, KernelBin: kernelBin, ToolsDir: toolsDir,
		Offline: true, ExtensionTrustedRoots: []string{extRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	sock1 := shortSocket(t)
	go func() { _ = d1.Run(sock1) }()
	waitForSocket(t, sock1)
	c1, err := rpc.Dial(sock1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"blocked", "free"} {
		var installed json.RawMessage
		if err := c1.Call("extension.install", map[string]any{"source": filepath.Join(extRoot, name)}, &installed); err != nil {
			t.Fatal(err)
		}
		if err := c1.Call("extension.enable", map[string]any{"name": name}, &installed); err != nil {
			t.Fatal(err)
		}
	}
	c1.Close()
	d1.Close()

	// Run 2: same state, now with an org policy that disables "blocked".
	policyDir := filepath.Join(stateDir, "policy")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "extensions.json"), []byte(`{"disabled":["blocked"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d2, err := daemon.New(daemon.Options{
		StateDir: stateDir, KernelBin: kernelBin, ToolsDir: toolsDir,
		Offline: true, ExtensionTrustedRoots: []string{extRoot}, PolicyDir: policyDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	sock2 := shortSocket(t)
	go func() { _ = d2.Run(sock2) }()
	waitForSocket(t, sock2)
	c2, err := rpc.Dial(sock2)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	// Startup reconcile forced the persisted enable off.
	var inv extensionInventory
	if err := c2.Call("extension.list", map[string]any{}, &inv); err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for i, p := range inv.Plugins {
		byName[p.Manifest.Name] = i
	}
	blocked := inv.Plugins[byName["blocked"]]
	free := inv.Plugins[byName["free"]]
	if blocked.Enabled || blocked.EffectiveEnabled || blocked.EnableProvenance != "org_policy" {
		t.Fatalf("startup reconcile did not force-disable: %+v", blocked)
	}
	if !free.Enabled || !free.EffectiveEnabled || free.EnableProvenance != "user" {
		t.Fatalf("unrelated extension was disturbed: %+v", free)
	}

	// Org-disabled extensions cannot be re-enabled from below.
	var re json.RawMessage
	err = c2.Call("extension.enable", map[string]any{"name": "blocked"}, &re)
	if err == nil || !strings.Contains(err.Error(), "organization policy") {
		t.Fatalf("expected org-policy rejection, got %v", err)
	}

	// A workspace_root-scoped list applies the project mask per request.
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".carina"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".carina", "extensions.json"), []byte(`{"disabled":["free"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var masked extensionInventory
	if err := c2.Call("extension.list", map[string]any{"workspace_root": ws}, &masked); err != nil {
		t.Fatal(err)
	}
	for _, p := range masked.Plugins {
		if p.Manifest.Name == "free" {
			if !p.Enabled || p.EffectiveEnabled || p.EnableProvenance != "project_policy" {
				t.Fatalf("project mask not applied: %+v", p)
			}
		}
		if p.Manifest.Name == "blocked" && p.EnableProvenance != "org_policy" {
			t.Fatalf("org tier must outrank project view: %+v", p)
		}
	}

	// The mask is a per-request view: a plain list is unchanged.
	var again extensionInventory
	if err := c2.Call("extension.list", map[string]any{}, &again); err != nil {
		t.Fatal(err)
	}
	for _, p := range again.Plugins {
		if p.Manifest.Name == "free" && (!p.Enabled || !p.EffectiveEnabled) {
			t.Fatalf("project mask leaked into daemon state: %+v", p)
		}
	}
}
