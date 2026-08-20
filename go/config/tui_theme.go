package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultTUITheme = "auto"

// TUIThemePreference is the highest-precedence persisted polarity
// preference. Theme is global by default and may be overridden per project.
type TUIThemePreference struct {
	Value       string
	PersistPath string
}

// InspectTUITheme reads only the theme polarity so unrelated daemon or
// first-run prerequisites cannot prevent the internal TUI from starting.
func InspectTUITheme(home, projectDir string) (TUIThemePreference, error) {
	globalPath := filepath.Join(home, ".carina", "config.json")
	pref := TUIThemePreference{Value: defaultTUITheme, PersistPath: globalPath}
	for _, path := range []string{
		globalPath,
		filepath.Join(projectDir, ".carina", "config.json"),
	} {
		value, found, err := readTUIThemeField(path)
		if err != nil {
			return TUIThemePreference{}, err
		}
		if !found {
			continue
		}
		value, err = normalizeTUITheme(value)
		if err != nil {
			return TUIThemePreference{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		pref.Value = value
		pref.PersistPath = path
	}
	return pref, nil
}

func normalizeTUITheme(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "auto", "dark", "light":
		return value, nil
	default:
		return "", fmt.Errorf("config: tui_theme must be one of auto, dark, light")
	}
}

func readTUIThemeField(path string) (string, bool, error) {
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
	raw, found := root["tui_theme"]
	if !found {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("config: parse %s tui_theme: %w", path, err)
	}
	return value, true, nil
}
