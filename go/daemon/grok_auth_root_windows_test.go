//go:build windows

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGrokAuthDirectoryTooBroadRejectsWindowsVolumeRoots(t *testing.T) {
	for _, path := range []string{
		`C:\`,
		`C:/`,
		`\\server\share`,
		`\\server\share\`,
		`\\?\C:\`,
		`\\?\UNC\server\share\`,
	} {
		if !grokAuthDirectoryTooBroad(path) {
			t.Fatalf("volume root %q was not treated as too broad", path)
		}
	}
	for _, path := range []string{
		`C:\Users\tester\.grok`,
		`\\server\share\tester\.grok`,
	} {
		if grokAuthDirectoryTooBroad(path) {
			t.Fatalf("owner-scoped directory %q was treated as too broad", path)
		}
	}
}

func TestWriteGrokSandboxConfigRejectsWindowsVolumeRootAuthPaths(t *testing.T) {
	for _, authPath := range []string{
		`C:\auth.json`,
		`\\server\share\auth.json`,
		`\\?\C:\auth.json`,
		`\\?\UNC\server\share\auth.json`,
	} {
		t.Run(authPath, func(t *testing.T) {
			isolatedHome := t.TempDir()
			if err := writeGrokSandboxConfig(isolatedHome, authPath); err == nil {
				t.Fatal("volume-root auth parent was added to the sandbox write allowlist")
			}
			if _, err := os.Stat(filepath.Join(isolatedHome, "sandbox.toml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe sandbox configuration was written: %v", err)
			}
		})
	}
}
