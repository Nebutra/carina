package statefmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbe(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		version int
		ok      bool
	}{
		{"stamped object", `{"version": 3, "records": []}`, 3, true},
		{"unstamped object", `{"records": []}`, 0, true},
		{"bare array is not an envelope", `[{"version": 9}]`, 0, false},
		{"scalar is not an envelope", `42`, 0, false},
		{"garbage is not an envelope", `{"version":`, 0, false},
		{"empty payload is not an envelope", ``, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version, ok := Probe([]byte(tc.raw))
			if version != tc.version || ok != tc.ok {
				t.Fatalf("Probe(%q) = (%d, %v), want (%d, %v)", tc.raw, version, ok, tc.version, tc.ok)
			}
		})
	}
}

func TestReadVersionedMissingFile(t *testing.T) {
	raw, version, ok := ReadVersioned(filepath.Join(t.TempDir(), "absent.json"), 1)
	if raw != nil || version != 0 || ok {
		t.Fatalf("missing file = (%q, %d, %v), want (nil, 0, false)", raw, version, ok)
	}
}

func TestReadVersionedLegacyUnstampedIsV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	payload := []byte(`{"records": [1, 2]}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, version, ok := ReadVersioned(path, 1)
	if !ok || version != 1 || string(raw) != string(payload) {
		t.Fatalf("legacy read = (%q, %d, %v), want payload as v1", raw, version, ok)
	}
}

func TestReadVersionedNonEnvelopeIsNeverGated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	payload := []byte(`["workspace-a", "workspace-b"]`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, version, ok := ReadVersioned(path, 1)
	if !ok || version != 1 || string(raw) != string(payload) {
		t.Fatalf("bare-array read = (%q, %d, %v), want pass-through", raw, version, ok)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("bare-array file must not be quarantined: %v", err)
	}
}

func TestReadVersionedEqualAndOlderPassThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version": 2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, version, ok := ReadVersioned(path, 2); !ok || version != 2 {
		t.Fatalf("equal version = (%d, %v), want (2, true)", version, ok)
	}
	if _, version, ok := ReadVersioned(path, 3); !ok || version != 2 {
		t.Fatalf("older version = (%d, %v), want (2, true)", version, ok)
	}
}

func TestReadVersionedFutureQuarantinesAndPreservesBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	payload := []byte(`{"version": 2, "records": ["from-the-future"]}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, version, ok := ReadVersioned(path, 1)
	if ok || raw != nil || version != 2 {
		t.Fatalf("future read = (%q, %d, %v), want (nil, 2, false)", raw, version, ok)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("future-version file must be moved aside, stat err = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly one quarantine file, got %d", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "state.json.v2.") || !strings.HasSuffix(name, ".quarantine") {
		t.Fatalf("quarantine name = %q", name)
	}
	kept, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != string(payload) {
		t.Fatalf("original bytes must be preserved, got %q", kept)
	}
}

func TestQuarantineRenamesAndReportsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := Quarantine(path, 7)
	if moved == "" {
		t.Fatal("Quarantine should report the new path")
	}
	if !strings.HasPrefix(filepath.Base(moved), "state.json.v7.") {
		t.Fatalf("quarantine path = %q", moved)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("quarantined file must exist: %v", err)
	}
	if Quarantine(filepath.Join(dir, "absent.json"), 7) != "" {
		t.Fatal("Quarantine of a missing file should report failure")
	}
}
