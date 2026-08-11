package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultTUIGlyphs = "auto"

// TUIGlyphsPreference is the highest-precedence persisted presentation
// preference. Glyphs are global by default and may be overridden per project.
type TUIGlyphsPreference struct {
	Value       string
	PersistPath string
}

// InspectTUIGlyphs reads only the glyph preference so unrelated daemon or
// first-run prerequisites cannot prevent the internal TUI from starting.
func InspectTUIGlyphs(home, projectDir string) (TUIGlyphsPreference, error) {
	globalPath := filepath.Join(home, ".carina", "config.json")
	pref := TUIGlyphsPreference{Value: defaultTUIGlyphs, PersistPath: globalPath}
	for _, path := range []string{
		globalPath,
		filepath.Join(projectDir, ".carina", "config.json"),
	} {
		value, found, err := readTUIGlyphsField(path)
		if err != nil {
			return TUIGlyphsPreference{}, err
		}
		if !found {
			continue
		}
		value, err = normalizeTUIGlyphs(value)
		if err != nil {
			return TUIGlyphsPreference{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		pref.Value = value
		pref.PersistPath = path
	}
	return pref, nil
}

func normalizeTUIGlyphs(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "auto", "unicode", "nerd", "ascii":
		return value, nil
	default:
		return "", fmt.Errorf("config: tui_glyphs must be one of auto, unicode, nerd, ascii")
	}
}

func readTUIGlyphsField(path string) (string, bool, error) {
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
	raw, found := root["tui_glyphs"]
	if !found {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("config: parse %s tui_glyphs: %w", path, err)
	}
	return value, true, nil
}
