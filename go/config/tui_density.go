package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TUIDensityPreference is the highest-precedence persisted presentation
// preference. Density is global by default and may be overridden per project.
type TUIDensityPreference struct {
	Value       string
	PersistPath string
}

// InspectTUIDensity reads only the density field so first-run locale or daemon
// prerequisites cannot prevent the internal TUI from starting.
func InspectTUIDensity(home, projectDir string) (TUIDensityPreference, error) {
	globalPath := filepath.Join(home, ".carina", "config.json")
	pref := TUIDensityPreference{Value: "compact", PersistPath: globalPath}
	for _, path := range []string{
		globalPath,
		filepath.Join(projectDir, ".carina", "config.json"),
	} {
		value, found, err := readTUIDensityField(path)
		if err != nil {
			return TUIDensityPreference{}, err
		}
		if found {
			pref.Value = value
			pref.PersistPath = path
		}
	}
	pref.Value = strings.ToLower(strings.TrimSpace(pref.Value))
	if pref.Value != "compact" && pref.Value != "comfortable" {
		return TUIDensityPreference{}, fmt.Errorf(
			"config: tui_density must be one of compact, comfortable",
		)
	}
	return pref, nil
}

func readTUIDensityField(path string) (string, bool, error) {
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
	raw, found := root["tui_density"]
	if !found {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("config: parse %s tui_density: %w", path, err)
	}
	return value, true, nil
}
