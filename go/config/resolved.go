package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// LayerProvenance records which keys were present in one config layer. Values
// are deliberately omitted so diagnostics cannot leak credentials or policy.
type LayerProvenance struct {
	Name   string   `json:"name"`
	Source string   `json:"source,omitempty"`
	Keys   []string `json:"keys,omitempty"`
}

type Provenance struct {
	Layers     []LayerProvenance `json:"layers"`
	KeySources map[string]string `json:"key_sources"`
}

type Resolved struct {
	Config      Config
	Locks       *LockReport
	Provenance  Provenance
	Fingerprint string
}

// LoadResolved resolves config with the platform managed path and records the
// same-layer provenance used to produce the returned Config.
func LoadResolved(home, projectDir string) (Resolved, error) {
	return LoadResolvedWithManaged(home, projectDir, DefaultManagedPath())
}

func LoadResolvedWithManaged(home, projectDir, managedPath string) (Resolved, error) {
	cfg, locks, provenance, err := loadResolved(home, projectDir, managedPath)
	if err != nil {
		return Resolved{Config: cfg, Locks: locks, Provenance: provenance}, err
	}
	fingerprint, err := Fingerprint(cfg)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Config: cfg, Locks: locks, Provenance: provenance,
		Fingerprint: fingerprint,
	}, nil
}

// Fingerprint hashes effective config without exposing values. Config contains
// credential references, not credential contents; callers expose only this
// digest in runtime metadata.
func Fingerprint(cfg Config) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("config: fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "cfg1_" + hex.EncodeToString(sum[:]), nil
}

func newProvenance() Provenance {
	return Provenance{KeySources: map[string]string{
		"state_dir": "default",
		"socket":    "default",
	}}
}

func (p *Provenance) addLayer(name, source string, keys []string) {
	if len(keys) == 0 {
		return
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	p.Layers = append(p.Layers, LayerProvenance{Name: name, Source: source, Keys: sorted})
	for _, key := range sorted {
		p.KeySources[key] = name
	}
}

func (p *Provenance) addEnvironment() {
	keys := make([]string, 0, 2)
	for key, env := range map[string]string{
		"state_dir": "CARINA_STATE_DIR",
		"socket":    "CARINA_SOCKET",
	} {
		if value, ok := os.LookupEnv(env); ok && strings.TrimSpace(value) != "" {
			keys = append(keys, key)
			p.KeySources[key] = "environment"
		}
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		p.Layers = append(p.Layers, LayerProvenance{Name: "environment", Keys: keys})
	}
}

func rawMessageKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
