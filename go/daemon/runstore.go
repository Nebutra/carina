package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
		if _, err := os.Stat(filepath.Join(r.dir, strings.TrimSuffix(e.Name(), ".json")+".tombstone")); err == nil {
			continue
		}
		if json.Unmarshal(raw, &t) == nil && t.TaskID != "" {
			out = append(out, &t)
		}
	}
	return out
}

func (r *runStore) tombstone(taskID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := filepath.Join(r.dir, taskID+".tombstone")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte("removed\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(r.dir, taskID+".json"))
	_ = os.Remove(filepath.Join(r.dir, taskID+".ckpt.json"))
	_ = os.RemoveAll(filepath.Join(r.dir, taskID+".ckpts"))
	return nil
}

func (r *runStore) writeRestoreJournal(taskID string, value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(r.dir, taskID+".restore.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
func (r *runStore) clearRestoreJournal(taskID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := os.Remove(filepath.Join(r.dir, taskID+".restore.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
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
	_ = r.saveCheckpointChecked(taskID, cp)
}

func (r *runStore) saveCheckpointChecked(taskID string, cp *runCheckpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	historyDir := filepath.Join(r.dir, taskID+".ckpts")
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		return err
	}
	historyPath := filepath.Join(historyDir, fmt.Sprintf("%020d.json", cp.Turn))
	historyTmp := historyPath + ".tmp"
	if err := os.WriteFile(historyTmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(historyTmp, historyPath); err != nil {
		return err
	}
	// Publish latest only after the immutable history entry is durable.
	p := filepath.Join(r.dir, taskID+".ckpt.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
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
