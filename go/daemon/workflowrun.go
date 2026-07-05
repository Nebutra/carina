package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// stepResult is the persisted outcome of one workflow step. Persisting these
// makes a workflow run resumable: a crashed or paused run reloads its
// completed steps and skips re-doing finished (and possibly costly) work.
type stepResult struct {
	Status string `json:"status"` // completed
	Output string `json:"output"`
}

// wfRunStore persists per-run step results as one JSON file per run under
// <stateDir>/wf-runs. It is safe for concurrent use by parallel steps.
type wfRunStore struct {
	mu  sync.Mutex
	dir string
}

func newWFRunStore(stateDir string) *wfRunStore {
	dir := filepath.Join(stateDir, "wf-runs")
	_ = os.MkdirAll(dir, 0o700)
	return &wfRunStore{dir: dir}
}

func (w *wfRunStore) path(runID string) string {
	return filepath.Join(w.dir, runID+".json")
}

// load returns the persisted step results for a run (empty map if none).
func (w *wfRunStore) load(runID string) map[string]stepResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]stepResult{}
	raw, err := os.ReadFile(w.path(runID))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// save atomically writes the full result set for a run (temp + rename).
func (w *wfRunStore) save(runID string, results map[string]stepResult) {
	w.mu.Lock()
	defer w.mu.Unlock()
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return
	}
	tmp := w.path(runID) + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, w.path(runID))
	}
}
