// Package extensions manages a local, source-trusted inventory of declarative
// extension bundles. Installation copies metadata only; executable components
// still run through Carina's governed WASM/MCP/worker adapters.
package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Dependency struct {
	Name       string `json:"name"`
	Constraint string `json:"constraint"`
}
type Manifest struct {
	Name                  string       `json:"name"`
	Version               string       `json:"version"`
	Description           string       `json:"description,omitempty"`
	RuntimeConstraint     string       `json:"runtime_constraint,omitempty"`
	Components            []string     `json:"components,omitempty"`
	Dependencies          []Dependency `json:"dependencies,omitempty"`
	Permissions           []string     `json:"permissions,omitempty"`
	EstimatedPromptTokens int          `json:"estimated_prompt_tokens,omitempty"`
}
type Installed struct {
	Manifest     Manifest  `json:"manifest"`
	Source       string    `json:"source"`
	SourceDigest string    `json:"source_digest"`
	Enabled      bool      `json:"enabled"`
	Trusted      bool      `json:"trusted"`
	InstalledAt  time.Time `json:"installed_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type InventoryEntry struct {
	Installed
	EffectiveEnabled bool   `json:"effective_enabled"`
	EnableProvenance string `json:"enable_provenance"`
}
type Inventory struct {
	Plugins           []InventoryEntry `json:"plugins"`
	SafeMode          bool             `json:"safe_mode"`
	TotalPromptTokens int              `json:"total_prompt_tokens"`
}
type Marketplace struct {
	mu             sync.Mutex
	path           string
	trustedRoots   []string
	plugins        map[string]Installed
	safeMode       bool
	orgPolicy      OrgExtensionPolicy
	runtimeVersion string
}

func New(stateDir, runtimeVersion string, trustedRoots []string) (*Marketplace, error) {
	m := &Marketplace{path: filepath.Join(stateDir, "extensions.json"), runtimeVersion: runtimeVersion, plugins: map[string]Installed{}}
	for _, r := range trustedRoots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, err
		}
		m.trustedRoots = append(m.trustedRoots, filepath.Clean(abs))
	}
	if raw, err := os.ReadFile(m.path); err == nil {
		var disk struct {
			Plugins  map[string]Installed `json:"plugins"`
			SafeMode bool                 `json:"safe_mode"`
		}
		if err := json.Unmarshal(raw, &disk); err != nil {
			return nil, err
		}
		m.plugins = disk.Plugins
		m.safeMode = disk.SafeMode
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return m, nil
}
func (m *Marketplace) Install(source string) (Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, err := filepath.Abs(source)
	if err != nil {
		return Installed{}, err
	}
	dir = filepath.Clean(dir)
	if !underAny(dir, m.trustedRoots) {
		return Installed{}, errors.New("extensions: source is not under a trusted root")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "carina-extension.json"))
	if err != nil {
		return Installed{}, fmt.Errorf("extensions: manifest: %w", err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Installed{}, fmt.Errorf("extensions: manifest: %w", err)
	}
	if err := validateManifest(man, m.runtimeVersion); err != nil {
		return Installed{}, err
	}
	if err := m.dependenciesAvailable(man); err != nil {
		return Installed{}, err
	}
	sum := sha256.Sum256(raw)
	now := time.Now().UTC()
	p := Installed{Manifest: man, Source: dir, SourceDigest: hex.EncodeToString(sum[:]), Enabled: false, Trusted: true, InstalledAt: now, UpdatedAt: now}
	if old, ok := m.plugins[man.Name]; ok {
		p.InstalledAt = old.InstalledAt
	}
	m.plugins[man.Name] = p
	return p, m.persistLocked()
}
func (m *Marketplace) Update(name string) (Installed, error) {
	m.mu.Lock()
	p, ok := m.plugins[name]
	m.mu.Unlock()
	if !ok {
		return Installed{}, os.ErrNotExist
	}
	return m.Install(p.Source)
}
func (m *Marketplace) SetEnabled(name string, on bool) (Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[name]
	if !ok {
		return Installed{}, os.ErrNotExist
	}
	if on && m.safeMode {
		return Installed{}, errors.New("extensions: safe mode disables all extensions")
	}
	if on && m.orgPolicy.disables(name) {
		return Installed{}, ErrOrgDisabled
	}
	p.Enabled = on
	p.UpdatedAt = time.Now().UTC()
	m.plugins[name] = p
	return p, m.persistLocked()
}
func (m *Marketplace) SetSafeMode(on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.safeMode = on
	if on {
		for name, p := range m.plugins {
			p.Enabled = false
			p.UpdatedAt = time.Now().UTC()
			m.plugins[name] = p
		}
	}
	return m.persistLocked()
}
func (m *Marketplace) Inventory() Inventory {
	return m.InventoryForWorkspace(ProjectExtensionPolicy{})
}
func (m *Marketplace) dependenciesAvailable(man Manifest) error {
	for _, d := range man.Dependencies {
		p, ok := m.plugins[d.Name]
		if !ok || !versionSatisfies(p.Manifest.Version, d.Constraint) {
			return fmt.Errorf("extensions: dependency %s %s is not installed", d.Name, d.Constraint)
		}
	}
	return nil
}
func validateManifest(m Manifest, runtime string) error {
	if m.Name == "" || m.Version == "" {
		return errors.New("extensions: name and version are required")
	}
	for _, c := range m.Components {
		switch c {
		case "skill", "hook", "mcp", "workflow", "wasm", "worker", "artifact-adapter":
		default:
			return fmt.Errorf("extensions: unsupported component %q", c)
		}
	}
	if !versionSatisfies(runtime, m.RuntimeConstraint) {
		return fmt.Errorf("extensions: runtime %s does not satisfy %s", runtime, m.RuntimeConstraint)
	}
	return nil
}
func underAny(path string, roots []string) bool {
	for _, r := range roots {
		rel, err := filepath.Rel(r, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
func versionSatisfies(version, constraint string) bool {
	if constraint == "" || constraint == "*" {
		return true
	}
	op := "="
	want := constraint
	for _, p := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(constraint, p) {
			op = p
			want = strings.TrimSpace(strings.TrimPrefix(constraint, p))
			break
		}
	}
	cmp := compareVersion(version, want)
	switch op {
	case ">=":
		return cmp >= 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	default:
		return cmp == 0
	}
}
func compareVersion(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
func (m *Marketplace) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(struct {
		Plugins  map[string]Installed `json:"plugins"`
		SafeMode bool                 `json:"safe_mode"`
	}{m.plugins, m.safeMode}, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
