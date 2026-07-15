package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UpdateTUIKeybinding atomically updates one project/global keybinding while
// preserving every unrelated config field. remove restores lower-layer/default
// behavior by deleting the concrete override.
func UpdateTUIKeybinding(path, action string, keys []string, remove bool) error {
	if strings.TrimSpace(action) == "" {
		return fmt.Errorf("config: keybinding action is required")
	}
	root := make(map[string]json.RawMessage)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(data) > 0 {
		if err := rejectDuplicateJSONKeys(data); err != nil {
			return fmt.Errorf("config: parse %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("config: parse %s: %w", path, err)
		}
	}

	bindings := make(map[string][]string)
	if raw := root["tui_keybindings"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &bindings); err != nil {
			return fmt.Errorf("config: parse %s tui_keybindings: %w", path, err)
		}
	}
	if remove {
		delete(bindings, action)
	} else {
		bindings[action] = append([]string(nil), keys...)
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return err
	}
	root["tui_keybindings"] = raw
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("config: chmod temporary file: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		return fmt.Errorf("config: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("config: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("config: replace %s: %w", path, err)
	}
	ok = true
	return nil
}
