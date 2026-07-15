package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUpdateTUIKeybindingPreservesConfigAndRemovesOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".carina", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"offline":true,"tui_keybindings":{"global.help":["f2"]}}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateTUIKeybinding(path, "composer.submit", []string{"ctrl+enter"}, false); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Offline        bool                `json:"offline"`
		TUIKeybindings map[string][]string `json:"tui_keybindings"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Offline || !reflect.DeepEqual(got.TUIKeybindings["composer.submit"], []string{"ctrl+enter"}) ||
		!reflect.DeepEqual(got.TUIKeybindings["global.help"], []string{"f2"}) {
		t.Fatalf("config update lost data: %+v", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("config mode = %v, err=%v", info.Mode().Perm(), err)
	}

	if err := UpdateTUIKeybinding(path, "global.help", nil, true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	got = struct {
		Offline        bool                `json:"offline"`
		TUIKeybindings map[string][]string `json:"tui_keybindings"`
	}{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, exists := got.TUIKeybindings["global.help"]; exists {
		t.Fatalf("custom binding was not removed: %+v", got.TUIKeybindings)
	}
}

func TestUpdateTUIKeybindingRejectsMalformedConfigWithoutReplacingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"offline":`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateTUIKeybinding(path, "global.help", []string{"f2"}, false); err == nil {
		t.Fatal("malformed config should be rejected")
	}
	got, _ := os.ReadFile(path)
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("malformed config was replaced: %q", got)
	}
}

func TestUpdateTUIKeybindingRejectsDuplicateKeysWithoutReplacingFile(t *testing.T) {
	tests := []struct {
		name     string
		original []byte
		key      string
	}{
		{name: "root", original: []byte(`{"offline":true,"offline":false}`), key: "offline"},
		{name: "tui action", original: []byte(`{"tui_keybindings":{"global.help":["f1"],"global.help":["f2"]}}`), key: "global.help"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, tt.original, 0o600); err != nil {
				t.Fatal(err)
			}
			err := UpdateTUIKeybinding(path, "composer.submit", []string{"ctrl+enter"}, false)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tt.key) || !strings.Contains(err.Error(), "remove one") {
				t.Fatalf("duplicate key error = %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.original) {
				t.Fatalf("duplicate-key config was replaced: %q", got)
			}
		})
	}
}
