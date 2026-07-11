package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

// Managed is the org-managed config overlay (by convention a root-owned file a
// non-admin user cannot edit, e.g. /etc/carina/managed.json). Values is an
// ordinary config-key object applied as a layer just above built-in defaults;
// every key named in LockedKeys is additionally re-applied after all other
// layers, so no global file, project file, or environment variable can
// override it (tighten-only: the managed layer can constrain, never unlock).
type Managed struct {
	Values     json.RawMessage `json:"values"`
	LockedKeys []string        `json:"locked_keys"`
}

// LockReport records which keys the managed file locked and where the locks
// came from, so collisions can be reported with provenance instead of being
// silently resolved.
type LockReport struct {
	// Source is the managed file path the locks were read from.
	Source string
	// Keys is the sorted, de-duplicated set of locked config keys.
	Keys []string

	set map[string]bool
}

// Locked reports whether key is managed-locked. Safe on a nil receiver (no
// managed file present means nothing is locked).
func (r *LockReport) Locked(key string) bool {
	return r != nil && r.set[key]
}

// DefaultManagedPath is the platform convention for the managed config file.
// It is deliberately not overridable by environment or config — an override
// knob would defeat the lock. Unix uses the same root-owned /etc/carina
// convention as the org policy directory.
func DefaultManagedPath() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "carina", "managed.json")
		}
		return `C:\ProgramData\carina\managed.json`
	}
	return "/etc/carina/managed.json"
}

// loadManaged parses and validates the managed file. An absent file yields
// (nil, nil) — the managed layer is optional and zero-cost when unused. A
// malformed or invalid file is a hard error, matching Load's fail-fast stance:
// an org that ships a managed file must not silently run without its locks.
func loadManaged(path string) (*Managed, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read managed %s: %w", path, err)
	}
	var m Managed
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("config: parse managed %s: %w", path, err)
	}
	if err := m.validate(path); err != nil {
		return nil, err
	}
	return &m, nil
}

// validate is fail-closed: a locked key that is not a known config key, or
// that has no corresponding entry in values, aborts startup rather than
// leaving an intended lock silently inert.
func (m *Managed) validate(path string) error {
	values, err := m.valueMap()
	if err != nil {
		return fmt.Errorf("config: managed %s: %w", path, err)
	}
	known := knownKeys()
	for key := range values {
		if !known[key] {
			return fmt.Errorf("config: managed %s: unknown key %q in values", path, key)
		}
	}
	for _, key := range m.LockedKeys {
		if !known[key] {
			return fmt.Errorf("config: managed %s: locked key %q is not a known config key", path, key)
		}
		if _, ok := values[key]; !ok {
			return fmt.Errorf("config: managed %s: locked key %q has no value in values", path, key)
		}
	}
	return nil
}

// valueMap parses Values into per-key raw messages (empty map when absent).
func (m *Managed) valueMap() (map[string]json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	if len(m.Values) == 0 {
		return values, nil
	}
	if err := json.Unmarshal(m.Values, &values); err != nil {
		return nil, fmt.Errorf("parse values: %w", err)
	}
	return values, nil
}

// apply overlays every managed value onto cfg (the layer just above defaults;
// non-locked values remain overridable by later layers).
func (m *Managed) apply(cfg *Config, path string) error {
	if len(m.Values) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(m.Values))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("config: apply managed %s: %w", path, err)
	}
	return nil
}

// applyLocked re-applies only the locked keys' managed values onto cfg, after
// every other layer has merged — the mechanism that makes locks final.
func (m *Managed) applyLocked(cfg *Config, path string) error {
	if len(m.LockedKeys) == 0 {
		return nil
	}
	values, err := m.valueMap()
	if err != nil {
		return fmt.Errorf("config: managed %s: %w", path, err)
	}
	subset := make(map[string]json.RawMessage, len(m.LockedKeys))
	for _, key := range m.LockedKeys {
		subset[key] = values[key]
	}
	data, err := json.Marshal(subset)
	if err != nil {
		return fmt.Errorf("config: managed %s: %w", path, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("config: re-apply locked keys from %s: %w", path, err)
	}
	return nil
}

// report builds the LockReport for the managed file at path.
func (m *Managed) report(path string) *LockReport {
	set := make(map[string]bool, len(m.LockedKeys))
	for _, key := range m.LockedKeys {
		set[key] = true
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return &LockReport{Source: path, Keys: keys, set: set}
}

// knownKeys derives the valid config-key set from Config's json struct tags,
// so new fields become lockable automatically and a typo in locked_keys is a
// startup error for free.
func knownKeys() map[string]bool {
	t := reflect.TypeOf(Config{})
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}
