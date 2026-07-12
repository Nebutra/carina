package daemon

import (
	"encoding/json"
	"fmt"
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

// persistedGeneratedStep is the durable part of a dynamic graph mutation.
// Generator output is journaled before the generator itself is committed as
// completed, so a crash can only cause an idempotent replay, never lose nodes
// that were already admitted to the run.
type persistedGeneratedStep struct {
	Step        WorkflowStep `json:"step"`
	GeneratorID string       `json:"generator_id"`
	Depth       int          `json:"depth"`
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

func (w *wfRunStore) graphPath(runID string) string {
	return filepath.Join(w.dir, runID+".graph.json")
}

// load returns the persisted step results for a run (empty map if none).
func (w *wfRunStore) load(runID string) (map[string]stepResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]stepResult{}
	raw, err := os.ReadFile(w.path(runID))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("workflow result journal corrupt: %w", err)
	}
	return out, nil
}

// save atomically writes the full result set for a run (temp + rename).
func (w *wfRunStore) save(runID string, results map[string]stepResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	tmp := w.path(runID) + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.path(runID))
}

func (w *wfRunStore) loadGenerated(runID string) ([]persistedGeneratedStep, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []persistedGeneratedStep
	raw, err := os.ReadFile(w.graphPath(runID))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("workflow generated-graph journal corrupt: %w", err)
	}
	return out, nil
}

func (w *wfRunStore) saveGenerated(runID string, steps []persistedGeneratedStep) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	raw, err := json.MarshalIndent(steps, "", "  ")
	if err != nil {
		return err
	}
	tmp := w.graphPath(runID) + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.graphPath(runID))
}
