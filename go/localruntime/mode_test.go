package localruntime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModeRequiresExplicitDecisionForLegacyState(t *testing.T) {
	t.Setenv(runtimeModeEnv, "")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".carina", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMode(home); !errors.Is(err, ErrModeDecisionRequired) {
		t.Fatalf("ResolveMode error = %v", err)
	}
	if err := WriteMode(home, ModeWorkspace); err != nil {
		t.Fatal(err)
	}
	mode, err := ResolveMode(home)
	if err != nil || mode != ModeWorkspace {
		t.Fatalf("persisted workspace mode = %q, %v", mode, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".carina", "state")); err != nil {
		t.Fatalf("legacy state changed: %v", err)
	}
}

func TestResolveModeEnvironmentOverrideDoesNotPersist(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".carina", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeModeEnv, string(ModeLegacy))
	mode, err := ResolveMode(home)
	if err != nil || mode != ModeLegacy {
		t.Fatalf("environment mode = %q, %v", mode, err)
	}
	if _, err := os.Stat(modeDecisionPath(home)); !os.IsNotExist(err) {
		t.Fatalf("environment override persisted a decision: %v", err)
	}
}
