package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeModeDisablesProjectHooks(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".carina"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `[{"event":"PreToolUse","matcher":"read","command":["sh","-c","echo blocked >&2; exit 2"]}]`
	if err := os.WriteFile(filepath.Join(ws, ".carina", "hooks.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{safeMode: true}
	if blocked, reason := d.runPreToolHooks(ws, "read", []byte(`{}`)); blocked || reason != "" {
		t.Fatalf("safe mode executed hook: blocked=%v reason=%q", blocked, reason)
	}
}
