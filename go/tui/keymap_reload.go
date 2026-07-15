package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/config"
)

const (
	keymapWatchPoll      = 200 * time.Millisecond
	keymapWatchStability = 600 * time.Millisecond
)

// KeymapReloadMsg carries an atomically validated keymap snapshot into the
// Bubble Tea update loop. Invalid saves are visible but leave the last-good
// bindings active.
type KeymapReloadMsg struct {
	Overrides []KeyBindingOverride
	Err       error
}

// WatchKeybindings watches every file-backed config layer used by the TUI.
// It waits for a stable signature before reading so truncate/write and
// temporary-file rename saves do not expose partial JSON to the running UI.
func WatchKeybindings(ctx context.Context, home, projectDir string, sender Sender) {
	if sender == nil {
		return
	}
	paths := []string{
		config.DefaultManagedPath(),
		filepath.Join(home, ".carina", "config.json"),
	}
	if projectDir != "" {
		paths = append(paths, filepath.Join(projectDir, ".carina", "config.json"))
	}
	applied := configFilesSignature(paths)
	candidate := applied
	candidateSince := time.Now()
	ticker := time.NewTicker(keymapWatchPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			signature := configFilesSignature(paths)
			if signature != candidate {
				candidate = signature
				candidateSince = now
				continue
			}
			if signature == applied || now.Sub(candidateSince) < keymapWatchStability {
				continue
			}
			cfg, err := config.Load(home, projectDir)
			var overrides []KeyBindingOverride
			if err == nil {
				overrides, err = ParseKeyBindingOverrides(cfg.TUIKeybindings)
			}
			sender.Send(KeymapReloadMsg{Overrides: overrides, Err: err})
			applied = signature
		}
	}
}

func configFilesSignature(paths []string) string {
	var b strings.Builder
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&b, "%s:missing;", path)
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d:%s;", path, info.Size(), info.ModTime().UnixNano(), info.Mode().String())
	}
	return b.String()
}
