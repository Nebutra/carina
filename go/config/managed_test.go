package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeManaged(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestManagedLockedKeySurvivesAllLayers: a locked key must beat the global
// file, the project file, and the environment; non-locked managed values stay
// an ordinary just-above-defaults layer that later layers may override.
func TestManagedLockedKeySurvivesAllLayers(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	proj := t.TempDir()

	managed := writeManaged(t, `{
		"values": {"sandbox_commands": true, "require_workspace_trust": true, "policy_dir": "/etc/carina/policy", "max_task_tokens": 5000},
		"locked_keys": ["sandbox_commands", "require_workspace_trust", "policy_dir"]
	}`)
	writeConfig(t, home, `{"sandbox_commands": false, "max_task_tokens": 100}`)
	writeConfig(t, proj, `{"require_workspace_trust": false, "policy_dir": "/p/policy"}`)
	t.Setenv("CARINA_SANDBOX_COMMANDS", "false")
	t.Setenv("CARINA_REQUIRE_WORKSPACE_TRUST", "false")
	t.Setenv("CARINA_POLICY_DIR", "/e/policy")

	cfg, locks, err := LoadWithManaged(home, proj, managed)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.SandboxCommands {
		t.Error("locked sandbox_commands must survive global-file and env overrides")
	}
	if !cfg.RequireWorkspaceTrust {
		t.Error("locked require_workspace_trust must survive project-file and env overrides")
	}
	if cfg.PolicyDir != "/etc/carina/policy" {
		t.Errorf("locked policy_dir must survive project-file and env overrides, got %q", cfg.PolicyDir)
	}
	// Non-locked managed value is overridable by a later layer.
	if cfg.MaxTaskTokens != 100 {
		t.Errorf("non-locked managed max_task_tokens must yield to the global file, want 100 got %d", cfg.MaxTaskTokens)
	}
	if locks == nil {
		t.Fatal("a managed file with locks must yield a LockReport")
	}
	if locks.Source != managed {
		t.Errorf("LockReport.Source = %q, want %q", locks.Source, managed)
	}
	want := []string{"policy_dir", "require_workspace_trust", "sandbox_commands"}
	if !reflect.DeepEqual(locks.Keys, want) {
		t.Errorf("LockReport.Keys = %v, want %v", locks.Keys, want)
	}
	if !locks.Locked("policy_dir") || locks.Locked("max_task_tokens") {
		t.Error("Locked() must report exactly the locked key set")
	}
}

// TestManagedReloadReappliesLocks: a fresh cascade run (what SIGHUP/auto-reload
// does) re-reads the managed file, so an edited lock set takes effect and the
// locked value keeps beating user layers on every reload.
func TestManagedReloadReappliesLocks(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	managed := writeManaged(t, `{
		"values": {"enable_egress_proxy": true},
		"locked_keys": ["enable_egress_proxy"]
	}`)
	writeConfig(t, home, `{"enable_egress_proxy": false}`)

	for i := 0; i < 2; i++ { // startup, then reload
		cfg, locks, err := LoadWithManaged(home, "", managed)
		if err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
		if !cfg.EnableEgressProxy {
			t.Fatalf("load %d: locked enable_egress_proxy must survive the global file", i)
		}
		if !locks.Locked("enable_egress_proxy") {
			t.Fatalf("load %d: lock report must persist across reloads", i)
		}
	}

	// Editing the managed file changes the lock set on the next reload.
	if err := os.WriteFile(managed, []byte(`{"values": {"enable_egress_proxy": true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, locks, err := LoadWithManaged(home, "", managed)
	if err != nil {
		t.Fatalf("reload after edit: %v", err)
	}
	if cfg.EnableEgressProxy {
		t.Error("after the lock is dropped, the global file must win again")
	}
	if locks.Locked("enable_egress_proxy") {
		t.Error("dropped lock must not be reported")
	}
}

// TestManagedAbsentIsZeroCost: no managed file means a nil report and a config
// identical to the plain cascade.
func TestManagedAbsentIsZeroCost(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	writeConfig(t, home, `{"offline": true}`)

	cfg, locks, err := LoadWithManaged(home, "", filepath.Join(t.TempDir(), "managed.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if locks != nil {
		t.Errorf("absent managed file must yield a nil LockReport, got %+v", locks)
	}
	plain, _, err := LoadWithManaged(home, "", "")
	if err != nil {
		t.Fatalf("plain load: %v", err)
	}
	if !reflect.DeepEqual(cfg, plain) {
		t.Errorf("absent managed file must not change the cascade:\n got %+v\nwant %+v", cfg, plain)
	}
	if locks.Locked("sandbox_commands") {
		t.Error("nil LockReport must report nothing locked")
	}
}

func TestManagedUnknownLockedKeyFailsStartup(t *testing.T) {
	scrubCarinaEnv(t)
	managed := writeManaged(t, `{"values": {"offline": true}, "locked_keys": ["ofline"]}`)
	_, _, err := LoadWithManaged(t.TempDir(), "", managed)
	if err == nil || !strings.Contains(err.Error(), `"ofline"`) {
		t.Fatalf("an unknown locked key must abort startup naming the key, got %v", err)
	}
}

func TestManagedLockedKeyWithoutValueFailsStartup(t *testing.T) {
	scrubCarinaEnv(t)
	managed := writeManaged(t, `{"values": {"offline": true}, "locked_keys": ["sandbox_commands"]}`)
	_, _, err := LoadWithManaged(t.TempDir(), "", managed)
	if err == nil || !strings.Contains(err.Error(), "no value") {
		t.Fatalf("a locked key without a managed value must abort startup, got %v", err)
	}
}

func TestManagedUnknownValueKeyFailsStartup(t *testing.T) {
	scrubCarinaEnv(t)
	managed := writeManaged(t, `{"values": {"typo_key": 1}}`)
	if _, _, err := LoadWithManaged(t.TempDir(), "", managed); err == nil {
		t.Fatal("an unknown key in managed values must be rejected (typo protection)")
	}
}

func TestManagedMalformedIsHardError(t *testing.T) {
	scrubCarinaEnv(t)
	managed := writeManaged(t, `{"values": {`)
	if _, _, err := LoadWithManaged(t.TempDir(), "", managed); err == nil {
		t.Fatal("a malformed managed file must fail fast, not be ignored")
	}
}

func TestManagedUnknownTopLevelFieldFailsStartup(t *testing.T) {
	scrubCarinaEnv(t)
	managed := writeManaged(t, `{"values": {}, "locked": ["offline"]}`)
	if _, _, err := LoadWithManaged(t.TempDir(), "", managed); err == nil {
		t.Fatal("an unknown top-level managed field must be rejected (locked_keys typo protection)")
	}
}

// TestKnownKeysCoversConfigFields: the reflected key set must track Config's
// json tags exactly, so new fields become lockable automatically.
func TestKnownKeysCoversConfigFields(t *testing.T) {
	known := knownKeys()
	typ := reflect.TypeOf(Config{})
	if len(known) != typ.NumField() {
		t.Fatalf("knownKeys has %d entries for %d Config fields", len(known), typ.NumField())
	}
	for _, key := range []string{"sandbox_commands", "policy_dir", "require_workspace_trust", "headroom_token_budget"} {
		if !known[key] {
			t.Errorf("knownKeys missing %q", key)
		}
	}
}

// TestWatchPathsIncludesManaged: managed-file edits must trigger auto-reload.
func TestWatchPathsIncludesManaged(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	paths := WatchPaths(home, proj, "/etc/carina/managed.json")
	want := []string{
		"/etc/carina/managed.json",
		filepath.Join(home, ".carina", "config.json"),
		filepath.Join(proj, ".carina", "config.json"),
	}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("WatchPaths = %v, want %v", paths, want)
	}
	if got := WatchPaths(home, "", ""); !reflect.DeepEqual(got, []string{filepath.Join(home, ".carina", "config.json")}) {
		t.Errorf("empty managed path must be skipped, got %v", got)
	}
}
