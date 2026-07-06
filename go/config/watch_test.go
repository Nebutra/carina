package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherPollDetectsChange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	os.WriteFile(p, []byte(`{"offline":false}`), 0o600)

	w := NewWatcher([]string{p}, time.Second, nil)
	prev := w.snapshot()

	if w.Poll(prev) {
		t.Fatal("no change should be detected against a fresh baseline")
	}
	// Bump mtime deterministically.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if !w.Poll(prev) {
		t.Fatal("an mtime change must be detected")
	}
	if w.Poll(prev) {
		t.Fatal("no further change after the previous poll consumed it")
	}
}

func TestWatcherDetectsFileAppearance(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	w := NewWatcher([]string{p}, time.Second, nil)
	prev := w.snapshot() // file absent
	os.WriteFile(p, []byte(`{}`), 0o600)
	if !w.Poll(prev) {
		t.Fatal("a newly-created config file must be detected")
	}
}

func TestWatcherRunFiresCallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	os.WriteFile(p, []byte(`{}`), 0o600)

	fired := make(chan struct{}, 1)
	w := NewWatcher([]string{p}, 10*time.Millisecond, func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	go w.Run()
	defer w.Stop()

	time.Sleep(20 * time.Millisecond) // let the baseline settle
	future := time.Now().Add(time.Hour)
	os.Chtimes(p, future, future)

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not fire onChange after a file change")
	}
}
