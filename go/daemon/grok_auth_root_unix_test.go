//go:build unix

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGrokAuthDirectoryTooBroadRejectsUnixRoot(t *testing.T) {
	for _, path := range []string{"", ".", "/", "//"} {
		if !grokAuthDirectoryTooBroad(path) {
			t.Fatalf("directory %q was not treated as too broad", path)
		}
	}
	if grokAuthDirectoryTooBroad(filepath.Join(t.TempDir(), ".grok")) {
		t.Fatal("owner-scoped Unix auth directory was treated as too broad")
	}
}

func TestWriteGrokSandboxConfigRejectsUnixRootAuthPath(t *testing.T) {
	isolatedHome := t.TempDir()
	if err := writeGrokSandboxConfig(isolatedHome, "/auth.json"); err == nil {
		t.Fatal("filesystem-root auth parent was added to the sandbox write allowlist")
	}
	if _, err := os.Stat(filepath.Join(isolatedHome, "sandbox.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe sandbox configuration was written: %v", err)
	}
}
