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
	"github.com/Nebutra/carina/go/statefmt"
)

// runStoreVersion is the on-disk format version stamped onto task records and
// run checkpoints. Per-store versioning (see go/statefmt): a future-stamped
// file is quarantined on read and the run resumes fresh rather than
// misreading a checkpoint written by a newer binary.
const runStoreVersion = 1

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
	row := struct {
		Version int `json:"version"`
		*scheduler.Task
	}{Version: runStoreVersion, Task: task}
	raw, err := json.MarshalIndent(row, "", "  ")
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
		if _, err := os.Stat(filepath.Join(r.dir, strings.TrimSuffix(e.Name(), ".json")+".tombstone")); err == nil {
			continue
		}
		raw, _, ok := statefmt.ReadVersioned(filepath.Join(r.dir, e.Name()), runStoreVersion)
		if !ok {
			continue
		}
		var t scheduler.Task
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

func (r *runStore) reconcileRestoreJournals() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	blocked := []string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".restore.json") {
			continue
		}
		p := filepath.Join(r.dir, e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var row map[string]any
		if json.Unmarshal(raw, &row) != nil {
			return nil, fmt.Errorf("restore journal %s is corrupt", e.Name())
		}
		row["state"] = "blocked_reconciliation_required"
		row["recovery_reason"] = "daemon restarted with an incomplete checkpoint restore"
		updated, err := json.MarshalIndent(row, "", "  ")
		if err != nil {
			return nil, err
		}
		tmp := p + ".tmp"
		if err = os.WriteFile(tmp, updated, 0o600); err != nil {
			return nil, err
		}
		if err = os.Rename(tmp, p); err != nil {
			return nil, err
		}
		blocked = append(blocked, strings.TrimSuffix(e.Name(), ".restore.json"))
	}
	return blocked, nil
}

// runCheckpoint is the resumable model-view of a run: the turn reached and the
// (compacted) transcript. The audit log remains the full source of truth; this
// is only what the agent loop needs to continue from where it left off.
type runCheckpoint struct {
	Version        int         `json:"version,omitempty"`
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
	row := *cp
	row.Version = runStoreVersion
	raw, err := json.Marshal(&row)
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
	return readRunCheckpoint(filepath.Join(r.dir, taskID+".ckpt.json"))
}

func (r *runStore) loadCheckpointTurn(taskID string, turn int) *runCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return readRunCheckpoint(filepath.Join(r.dir, taskID+".ckpts", fmt.Sprintf("%020d.json", turn)))
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

// readRunCheckpoint reads one checkpoint file. A checkpoint stamped by a
// newer binary is quarantined (never deleted) and nil is returned, so the
// resume path falls back to a fresh start instead of misreading a future
// format.
func readRunCheckpoint(path string) *runCheckpoint {
	raw, _, ok := statefmt.ReadVersioned(path, runStoreVersion)
	if !ok {
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
