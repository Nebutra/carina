package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nebutra/carina/go/microcopy"
)

// TUILocalePreference describes the highest-precedence editable locale value.
// It intentionally carries source metadata without copying unrelated config.
type TUILocalePreference struct {
	Value       string
	Canonical   string
	Source      string
	PersistPath string
	Valid       bool
}

// InspectTUILocale reads only the global and project locale fields so an
// interactive launcher can repair an invalid locale before full config
// validation. CARINA_TUI_LOCALE remains an explicit non-file override.
func InspectTUILocale(home, projectDir string) (TUILocalePreference, error) {
	globalPath := filepath.Join(home, ".carina", "config.json")
	pref := TUILocalePreference{PersistPath: globalPath}
	for _, layer := range []struct {
		name string
		path string
	}{
		{name: "global", path: globalPath},
		{name: "project", path: filepath.Join(projectDir, ".carina", "config.json")},
	} {
		value, found, err := readTUILocaleField(layer.path)
		if err != nil {
			return TUILocalePreference{}, err
		}
		if found {
			pref.Value = value
			pref.Source = layer.name
			pref.PersistPath = layer.path
		}
	}
	if value, ok := os.LookupEnv("CARINA_TUI_LOCALE"); ok && strings.TrimSpace(value) != "" {
		pref.Value = value
		pref.Source = "environment"
		pref.PersistPath = ""
	}
	if strings.TrimSpace(pref.Value) == "" {
		return pref, nil
	}
	canonical, err := microcopy.CanonicalLocale(pref.Value)
	if err != nil {
		return pref, nil
	}
	pref.Canonical = canonical
	pref.Valid = true
	return pref, nil
}

// UpdateTUILocale atomically updates one config file while preserving every
// unrelated field.
func UpdateTUILocale(path, locale string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config: locale persistence path is required")
	}
	canonical, err := microcopy.CanonicalLocale(locale)
	if err != nil {
		return err
	}
	return updateConfigRoot(path, func(root map[string]json.RawMessage) error {
		raw, err := json.Marshal(canonical)
		if err != nil {
			return err
		}
		root["tui_locale"] = raw
		return nil
	})
}

func readTUILocaleField(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return "", false, fmt.Errorf("config: parse %s: %w", path, err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return "", false, fmt.Errorf("config: parse %s: %w", path, err)
	}
	raw, found := root["tui_locale"]
	if !found {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("config: parse %s tui_locale: %w", path, err)
	}
	return value, true, nil
}
