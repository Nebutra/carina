package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/config"
)

// lockReportFor builds a real LockReport by loading a managed file that locks
// the given boolean keys (LockReport's lookup set is package-private to
// go/config).
func lockReportFor(t *testing.T, boolKeys ...string) *config.LockReport {
	t.Helper()
	body := `{"values": {`
	for i, k := range boolKeys {
		if i > 0 {
			body += ", "
		}
		body += `"` + k + `": true`
	}
	body += `}, "locked_keys": ["` + strings.Join(boolKeys, `", "`) + `"]}`
	path := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, locks, err := config.LoadWithManaged(t.TempDir(), "", path)
	if err != nil {
		t.Fatalf("load managed fixture: %v", err)
	}
	return locks
}

func TestValidateLockedFlags(t *testing.T) {
	locks := lockReportFor(t, "sandbox_commands", "require_workspace_trust")

	// No managed file at all: any flag is fine.
	if err := validateLockedFlags(map[string]bool{"sandbox": true}, nil); err != nil {
		t.Errorf("nil lock report must allow all flags, got %v", err)
	}
	// Explicit flag colliding with a locked key fails closed with provenance.
	err := validateLockedFlags(map[string]bool{"sandbox": true}, locks)
	if err == nil {
		t.Fatal("explicitly-set -sandbox must collide with locked sandbox_commands")
	}
	for _, want := range []string{"-sandbox", `"sandbox_commands"`, locks.Source} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("collision error must name %q, got %v", want, err)
		}
	}
	// Flags for unlocked keys, and flags with no config counterpart, pass.
	if err := validateLockedFlags(map[string]bool{"max-task-tokens": true, "safe-mode": true}, locks); err != nil {
		t.Errorf("unlocked/config-less flags must pass, got %v", err)
	}
	// Second locked key collides too.
	if err := validateLockedFlags(map[string]bool{"require-trust": true}, locks); err == nil {
		t.Error("explicitly-set -require-trust must collide with locked require_workspace_trust")
	}
}

// TestFlagConfigKeysAreKnown: every mapped config key must pass go/config's
// known-key validation, so the collision check can never dangle on a typo.
// A managed file locking a known key without a value fails with "no value";
// an unknown key fails earlier with "not a known config key".
func TestFlagConfigKeysAreKnown(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	for flagName, key := range flagConfigKeys {
		path := filepath.Join(dir, key+".json")
		body := `{"values": {}, "locked_keys": ["` + key + `"]}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := config.LoadWithManaged(home, "", path)
		if err == nil || !strings.Contains(err.Error(), "no value") {
			t.Errorf("flag -%s maps to %q, which go/config does not recognize as a config key (err: %v)", flagName, key, err)
		}
	}
}

func TestValidateListenerSecurity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tcp     string
		ws      string
		keyFile string
		wantErr string
	}{
		{name: "local defaults"},
		{name: "loopback tcp", tcp: "127.0.0.1:7777"},
		{name: "authenticated websocket", ws: "127.0.0.1:8777", keyFile: "/private/key"},
		{name: "wildcard tcp", tcp: ":7777", wantErr: "restricted to explicit loopback"},
		{name: "all interfaces tcp", tcp: "0.0.0.0:7777", wantErr: "restricted to explicit loopback"},
		{name: "anonymous websocket", ws: "127.0.0.1:8777", wantErr: "gateway websocket requires"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateListenerSecurity(tc.tcp, tc.ws, tc.keyFile)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
