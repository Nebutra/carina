package config

import (
	"os"
	"path/filepath"
	"time"
)

// Watcher polls config files for modification and invokes onChange when any of
// them changes. It is dependency-free (mtime polling) — an auto-reload path for
// environments that can't send SIGHUP.
type Watcher struct {
	paths    []string
	interval time.Duration
	onChange func()
	stop     chan struct{}
}

// WatchPaths returns the config files the cascade reads for a home+project
// pair plus the managed file ("" skips it), so managed-file edits trigger the
// same auto-reload as user config edits.
func WatchPaths(home, projectDir, managedPath string) []string {
	var paths []string
	if managedPath != "" {
		paths = append(paths, managedPath)
	}
	paths = append(paths, filepath.Join(home, ".carina", "config.json"))
	if projectDir != "" {
		paths = append(paths, filepath.Join(projectDir, ".carina", "config.json"))
	}
	return paths
}

// NewWatcher builds a watcher over paths, polling every interval.
func NewWatcher(paths []string, interval time.Duration, onChange func()) *Watcher {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	return &Watcher{paths: paths, interval: interval, onChange: onChange, stop: make(chan struct{})}
}

// snapshot records each path's mtime (0 for an absent file).
func (w *Watcher) snapshot() map[string]int64 {
	m := make(map[string]int64, len(w.paths))
	for _, p := range w.paths {
		if fi, err := os.Stat(p); err == nil {
			m[p] = fi.ModTime().UnixNano()
		} else {
			m[p] = 0
		}
	}
	return m
}

// Poll reports whether any watched file changed since prev (which it updates).
// Exposed for deterministic testing.
func (w *Watcher) Poll(prev map[string]int64) bool {
	changed := false
	for p, t := range w.snapshot() {
		if prev[p] != t {
			changed = true
			prev[p] = t
		}
	}
	return changed
}

// Run polls until Stop; the first snapshot is the baseline (no immediate fire).
func (w *Watcher) Run() {
	prev := w.snapshot()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if w.Poll(prev) && w.onChange != nil {
				w.onChange()
			}
		}
	}
}

// Stop ends the watch loop.
func (w *Watcher) Stop() { close(w.stop) }
