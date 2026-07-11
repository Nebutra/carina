// Package statefmt is the shared mechanism for versioned on-disk state:
// object-envelope JSON stores stamp a "version" field on write and, on read,
// quarantine (never delete) files stamped with a version newer than the
// running binary understands. Versioning is per-store — each store keeps its
// own version constant and passes it as current — so stores evolve
// independently instead of moving in lockstep behind one global number.
//
// The rules are deliberately asymmetric so no compatible file is ever falsely
// blocked: a missing or zero version is treated as v1 legacy, a bare-array or
// otherwise non-envelope payload is never version-gated, and versions at or
// below current pass through untouched. Only version > current fails closed,
// which is precisely the case value-inspecting reads are structurally unable
// to handle — an old binary cannot inspect its way around a format it does
// not know yet, but it can refuse to destroy it.
//
// This package must stay a leaf (stdlib-only imports) so go/daemon,
// go/session-store, and go/scheduler can all use it without import cycles.
package statefmt

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Probe inspects a raw payload for a version envelope. It returns
// (version, true) for top-level JSON objects — version 0 when the field is
// absent — and (0, false) for anything that is not an object (bare arrays,
// scalars, garbage), meaning "not an envelope, do not version-gate".
func Probe(raw []byte) (version int, ok bool) {
	var env struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, false
	}
	return env.Version, true
}

// ReadVersioned reads a state file and applies the version gate:
//
//   - missing file → (nil, 0, false)
//   - non-envelope payload or version 0/absent → treated as v1 legacy;
//     payload is returned and never blocked
//   - version <= current → payload and version returned
//   - version > current → the file is quarantined in place (fail-closed
//     against downgrade or foreign-future files) and ok is false
//
// Callers still unmarshal the returned payload themselves; a payload that
// passes the gate but fails to decode is corrupt, not future-versioned, and
// should be handed to Quarantine by the caller rather than overwritten.
func ReadVersioned(path string, current int) (raw []byte, version int, ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false
	}
	version, isEnvelope := Probe(raw)
	if !isEnvelope || version == 0 {
		return raw, 1, true
	}
	if version > current {
		Quarantine(path, version)
		return nil, version, false
	}
	return raw, version, true
}

// Quarantine renames a state file aside — never deletes — mirroring the
// scheduler's ".corrupt.<nanos>" idiom, with the offending version embedded
// so the original bytes survive for a newer binary or a human to recover.
// It returns the quarantine path so callers can log it, or "" if the rename
// failed (in which case the original file is left untouched).
func Quarantine(path string, version int) string {
	quarantined := fmt.Sprintf("%s.v%d.%d.quarantine", path, version, time.Now().UTC().UnixNano())
	if err := os.Rename(path, quarantined); err != nil {
		return ""
	}
	return quarantined
}
