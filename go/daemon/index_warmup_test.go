package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func TestIndexWarmupAllowedRefusesHomeAndRoot(t *testing.T) {
	if indexWarmupAllowed("/") {
		t.Fatal("filesystem root must not be idle-indexed")
	}
	if indexWarmupAllowed("") {
		t.Fatal("empty root must not be idle-indexed")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	if indexWarmupAllowed(home) {
		t.Fatal("home directory must not be idle-indexed")
	}
	if !indexWarmupAllowed(t.TempDir()) {
		t.Fatal("a project workspace must be eligible for idle warmup")
	}
}

func TestSessionCreateWarmsProjectIndex(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	if err := os.WriteFile(filepath.Join(ws, "main.rs"), []byte("fn warmup_marker() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessAny, err := d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": ws,
		"profile":        "safe-edit",
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess := sessAny.(*sessionstore.Session)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := d.indexBuilt.Load(sess.SessionID); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("idle warmup did not build a project index")
}
