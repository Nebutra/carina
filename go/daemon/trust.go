package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// trustStore persists the set of workspace roots the operator has explicitly
// trusted. When strict trust is enabled, command execution in an untrusted
// workspace is refused until it is approved — a defense against an agent (or a
// crafted session/workflow) running code in a directory the operator never
// vetted (the "workspace trust" prompt other tools show on first open).
type trustStore struct {
	mu      sync.Mutex
	path    string
	trusted map[string]bool
}

func newTrustStore(stateDir string) *trustStore {
	ts := &trustStore{path: filepath.Join(stateDir, "trust.json"), trusted: map[string]bool{}}
	if raw, err := os.ReadFile(ts.path); err == nil {
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			for _, r := range list {
				ts.trusted[r] = true
			}
		}
	}
	return ts
}

func (t *trustStore) isTrusted(root string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.trusted[root]
}

func (t *trustStore) setTrust(root string, trusted bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if trusted {
		t.trusted[root] = true
	} else {
		delete(t.trusted, root)
	}
	list := make([]string, 0, len(t.trusted))
	for r := range t.trusted {
		list = append(list, r)
	}
	raw, _ := json.MarshalIndent(list, "", "  ")
	tmp := t.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, t.path)
	}
}
