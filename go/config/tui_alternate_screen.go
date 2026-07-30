package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InspectTUIAlternateScreen reads only terminal-mode configuration so invalid
// first-run prerequisites such as locale can still be repaired in-product.
func InspectTUIAlternateScreen(home, projectDir string) (string, error) {
	mode := "auto"
	for _, path := range []string{
		filepath.Join(home, ".carina", "config.json"),
		filepath.Join(projectDir, ".carina", "config.json"),
	} {
		value, found, err := readTUIAlternateScreenField(path)
		if err != nil {
			return "", err
		}
		if found {
			mode = value
		}
	}
	if value, ok := os.LookupEnv("CARINA_TUI_ALTERNATE_SCREEN"); ok && strings.TrimSpace(value) != "" {
		mode = value
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "auto" && mode != "always" && mode != "never" {
		return "", fmt.Errorf("config: tui_alternate_screen must be one of auto, always, never")
	}
	return mode, nil
}

func readTUIAlternateScreenField(path string) (string, bool, error) {
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
	raw, found := root["tui_alternate_screen"]
	if !found {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("config: parse %s tui_alternate_screen: %w", path, err)
	}
	return value, true, nil
}
