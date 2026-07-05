package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/Nebutra/carina/go/scheduler"
)

// runStore persists background-run records — one JSON file per task under
// <stateDir>/runs — so the run registry (status, summary, applied patches)
// survives a daemon restart. This is the durable, queryable *record* of a run;
// resuming the live agent loop is a separate concern (transcript checkpoint).
type runStore struct {
	mu  sync.Mutex
	dir string
}

func newRunStore(stateDir string) *runStore {
	dir := filepath.Join(stateDir, "runs")
	_ = os.MkdirAll(dir, 0o700)
	return &runStore{dir: dir}
}

// save atomically writes a task record (temp + rename).
func (r *runStore) save(task *scheduler.Task) {
	if task == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return
	}
	p := filepath.Join(r.dir, task.TaskID+".json")
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// load reads all persisted task records (for run-registry recovery on startup).
func (r *runStore) load() []*scheduler.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	var out []*scheduler.Task
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
		if err != nil {
			continue
		}
		var t scheduler.Task
		if json.Unmarshal(raw, &t) == nil && t.TaskID != "" {
			out = append(out, &t)
		}
	}
	return out
}
