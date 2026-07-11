package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// runCheckpoint is the resumable model-view of a run: the turn reached and the
// (compacted) transcript. The audit log remains the full source of truth; this
// is only what the agent loop needs to continue from where it left off.
type runCheckpoint struct {
	Turn           int         `json:"turn"`
	Transcript     *Transcript `json:"transcript"`
	MemorySnapshot string      `json:"memory_snapshot,omitempty"`
	AppliedPatches []string    `json:"applied_patches,omitempty"`
}

func (r *runStore) saveCheckpoint(taskID string, cp *runCheckpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := json.Marshal(cp)
	if err != nil {
		return
	}
	p := filepath.Join(r.dir, taskID+".ckpt.json")
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
	historyDir := filepath.Join(r.dir, taskID+".ckpts")
	if os.MkdirAll(historyDir, 0o700) != nil {
		return
	}
	historyPath := filepath.Join(historyDir, fmt.Sprintf("%020d.json", cp.Turn))
	historyTmp := historyPath + ".tmp"
	if os.WriteFile(historyTmp, raw, 0o600) == nil {
		_ = os.Rename(historyTmp, historyPath)
	}
}

func (r *runStore) loadCheckpoint(taskID string) *runCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(r.dir, taskID+".ckpt.json"))
	if err != nil {
		return nil
	}
	return decodeRunCheckpoint(raw)
}

func (r *runStore) loadCheckpointTurn(taskID string, turn int) *runCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(r.dir, taskID+".ckpts", fmt.Sprintf("%020d.json", turn)))
	if err != nil {
		return nil
	}
	return decodeRunCheckpoint(raw)
}

func (r *runStore) listCheckpoints(taskID string) []*runCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	dir := filepath.Join(r.dir, taskID+".ckpts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if cp := readRunCheckpoint(filepath.Join(r.dir, taskID+".ckpt.json")); cp != nil {
			return []*runCheckpoint{cp}
		}
		return nil
	}
	out := make([]*runCheckpoint, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if cp := readRunCheckpoint(filepath.Join(dir, entry.Name())); cp != nil {
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Turn < out[j].Turn })
	return out
}

func readRunCheckpoint(path string) *runCheckpoint {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return decodeRunCheckpoint(raw)
}

func decodeRunCheckpoint(raw []byte) *runCheckpoint {
	var cp runCheckpoint
	if json.Unmarshal(raw, &cp) != nil || cp.Transcript == nil {
		return nil
	}
	cp.Transcript.policy = defaultCompactionPolicy()
	return &cp
}

func (r *runStore) deleteCheckpoint(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = os.Remove(filepath.Join(r.dir, taskID+".ckpt.json"))
	_ = os.RemoveAll(filepath.Join(r.dir, taskID+".ckpts"))
}
