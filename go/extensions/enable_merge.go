// Tri-level extension enable-merge: safe_mode > org > project > user.
//
// The org tier is a managed file under the daemon's PolicyDir
// (<PolicyDir>/extensions.json) and the project tier is a per-request,
// never-persisted mask read from <workspaceRoot>/.carina/extensions.json.
// Both tiers are disable-only by schema: they can switch extensions off but
// structurally cannot enable anything, so a malicious repository file is at
// worst a bounded denial of service, never a privilege gain. An extension is
// effectively enabled only when the user enabled it AND no tier disables it —
// tighten-only intersection semantics, matching the kernel PolicyBundle.
package extensions

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Enable-provenance values reported in Inventory entries: the
// highest-precedence tier that determines the effective enable state.
const (
	ProvenanceUser          = "user"
	ProvenanceOrgPolicy     = "org_policy"
	ProvenanceProjectPolicy = "project_policy"
	ProvenanceSafeMode      = "safe_mode"
)

// ErrOrgDisabled is returned by SetEnabled when the organization policy
// disables the extension: the org tier cannot be re-enabled from below,
// mirroring the safe-mode guard.
var ErrOrgDisabled = errors.New("extensions: organization policy disables this extension")

// OrgExtensionPolicy is the managed org tier, loaded from
// <PolicyDir>/extensions.json. The zero value disables nothing.
type OrgExtensionPolicy struct {
	Disabled   []string `json:"disabled"`
	DisableAll bool     `json:"disable_all"`
}

// ProjectExtensionPolicy is the repository-local tier, loaded from
// <workspaceRoot>/.carina/extensions.json. It is a computed per-request mask,
// never persisted daemon state. The zero value disables nothing.
type ProjectExtensionPolicy struct {
	Disabled []string `json:"disabled"`
}

// projectPolicyMaxBytes caps the project policy read so an untrusted
// repository cannot feed the daemon an arbitrarily large file.
const projectPolicyMaxBytes = 64 << 10

// LoadOrgPolicy reads the org extension policy from <dir>/extensions.json.
// An empty dir or a missing file yields the zero policy, preserving the
// loadOrgPolicy contract that open-source/local use pays no cost. A present
// but unreadable or malformed managed file fails closed to DisableAll: a
// broken org lockdown must never silently loosen into no lockdown.
func LoadOrgPolicy(dir string) OrgExtensionPolicy {
	if dir == "" {
		return OrgExtensionPolicy{}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "extensions.json"))
	if errors.Is(err, os.ErrNotExist) {
		return OrgExtensionPolicy{}
	}
	if err != nil {
		return OrgExtensionPolicy{DisableAll: true}
	}
	var disk OrgExtensionPolicy
	if err := json.Unmarshal(raw, &disk); err != nil {
		return OrgExtensionPolicy{DisableAll: true}
	}
	return disk
}

// LoadProjectPolicy reads the project extension policy from
// <workspaceRoot>/.carina/extensions.json with a size-capped read. Missing,
// oversized, or malformed files are treated as the empty policy: because the
// tier is disable-only, failing to a no-op cannot loosen anything, which is
// why no workspace-trust gate is needed.
func LoadProjectPolicy(workspaceRoot string) ProjectExtensionPolicy {
	if workspaceRoot == "" {
		return ProjectExtensionPolicy{}
	}
	f, err := os.Open(filepath.Join(workspaceRoot, ".carina", "extensions.json"))
	if err != nil {
		return ProjectExtensionPolicy{}
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, projectPolicyMaxBytes+1))
	if err != nil || len(raw) > projectPolicyMaxBytes {
		return ProjectExtensionPolicy{}
	}
	var disk ProjectExtensionPolicy
	if err := json.Unmarshal(raw, &disk); err != nil {
		return ProjectExtensionPolicy{}
	}
	return disk
}

func (p OrgExtensionPolicy) disables(name string) bool {
	if p.DisableAll {
		return true
	}
	return containsName(p.Disabled, name)
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// EffectiveEnabled merges the four tiers with precedence
// safe_mode > org > project > user. The result is true only when the user
// enabled the extension and no higher tier disables it; the returned
// provenance names the highest-precedence tier that decided the outcome.
func EffectiveEnabled(userEnabled bool, name string, safeMode bool, org OrgExtensionPolicy, proj ProjectExtensionPolicy) (bool, string) {
	if safeMode {
		return false, ProvenanceSafeMode
	}
	if org.disables(name) {
		return false, ProvenanceOrgPolicy
	}
	if containsName(proj.Disabled, name) {
		return false, ProvenanceProjectPolicy
	}
	return userEnabled, ProvenanceUser
}

// SetOrgPolicy installs the org tier and reconciles persisted state: any
// entry the org disables is forced to Enabled=false (the same sweep pattern
// as SetSafeMode) and the inventory is persisted atomically when it changed.
func (m *Marketplace) SetOrgPolicy(p OrgExtensionPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orgPolicy = p
	changed := false
	for name, inst := range m.plugins {
		if inst.Enabled && p.disables(name) {
			inst.Enabled = false
			inst.UpdatedAt = time.Now().UTC()
			m.plugins[name] = inst
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return m.persistLocked()
}

// InventoryForWorkspace returns the inventory with effective enable state
// computed against the daemon org policy plus the caller-supplied project
// mask. The project tier only shapes this response — it is never persisted.
func (m *Marketplace) InventoryForWorkspace(proj ProjectExtensionPolicy) Inventory {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Inventory{SafeMode: m.safeMode}
	for name, p := range m.plugins {
		eff, prov := EffectiveEnabled(p.Enabled, name, m.safeMode, m.orgPolicy, proj)
		out.Plugins = append(out.Plugins, InventoryEntry{Installed: p, EffectiveEnabled: eff, EnableProvenance: prov})
		if eff {
			out.TotalPromptTokens += p.Manifest.EstimatedPromptTokens
		}
	}
	sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].Manifest.Name < out.Plugins[j].Manifest.Name })
	return out
}
