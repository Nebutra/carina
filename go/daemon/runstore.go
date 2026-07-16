package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	"github.com/Nebutra/carina/go/statefmt"
)

// Task rows remain v1. Checkpoints evolved independently to v2 when durable
// ids, lineage, timestamps, and sequences were added. A v2 reader accepts v1
// checkpoints, while a v1 binary quarantines v2 rather than misreading them.
const runStoreVersion = 1
const checkpointStoreVersion = 2

// runStore persists background-run records — one JSON file per task under
// <stateDir>/runs — so the run registry (status, summary, applied patches)
// survives a daemon restart. This is the durable, queryable *record* of a run;
// resuming the live agent loop is a separate concern (transcript checkpoint).
type runStore struct {
	mu                      sync.Mutex
	dir                     string
	lastCheckpointSequence  int64
	restoreJournalWriteHook func(taskID string, journal *restoreJournal) error
}

const restoreJournalVersion = 1

type restoreJournal struct {
	Version              int      `json:"version"`
	OperationID          string   `json:"operation_id"`
	CheckpointID         string   `json:"checkpoint_id"`
	TargetTurn           int      `json:"target_turn"`
	TargetAppliedPatches []string `json:"target_applied_patches,omitempty"`
	Pending              []string `json:"pending,omitempty"`
	Completed            []string `json:"completed,omitempty"`
	State                string   `json:"state"`
	Failure              string   `json:"failure,omitempty"`
	RecoveryAction       string   `json:"recovery_action,omitempty"`
	UpdatedAt            string   `json:"updated_at"`
}

func newRunStore(stateDir string) *runStore {
	dir := filepath.Join(stateDir, "runs")
	_ = os.MkdirAll(dir, 0o700)
	r := &runStore{dir: dir}
	r.recoverCompactJournals()
	return r
}

// save atomically writes a task record (temp + rename).
func (r *runStore) save(task *scheduler.Task) {
	_ = r.saveChecked(task)
}

func (r *runStore) saveChecked(task *scheduler.Task) error {
	if task == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	row := struct {
		Version                     int    `json:"version"`
		ClientSubmissionFingerprint string `json:"client_submission_fingerprint,omitempty"`
		*scheduler.Task
	}{
		Version:                     runStoreVersion,
		ClientSubmissionFingerprint: task.ClientSubmissionFingerprint,
		Task:                        task,
	}
	raw, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(r.dir, task.TaskID+".json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
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
		if strings.HasSuffix(e.Name(), ".ckpt.json") || strings.HasSuffix(e.Name(), ".restore.json") {
			continue
		}
		if _, err := os.Stat(filepath.Join(r.dir, strings.TrimSuffix(e.Name(), ".json")+".tombstone")); err == nil {
			continue
		}
		raw, _, ok := statefmt.ReadVersioned(filepath.Join(r.dir, e.Name()), runStoreVersion)
		if !ok {
			continue
		}
		var row struct {
			ClientSubmissionFingerprint string `json:"client_submission_fingerprint"`
			scheduler.Task
		}
		if json.Unmarshal(raw, &row) == nil && row.TaskID != "" {
			row.Task.ClientSubmissionFingerprint = row.ClientSubmissionFingerprint
			out = append(out, &row.Task)
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
	if journal, ok := value.(*restoreJournal); ok && r.restoreJournalWriteHook != nil {
		if err := r.restoreJournalWriteHook(taskID, journal); err != nil {
			return err
		}
	}
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

func (r *runStore) isTombstoned(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := os.Stat(filepath.Join(r.dir, taskID+".tombstone"))
	return err == nil
}

func (r *runStore) loadRestoreJournal(taskID string) (*restoreJournal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(r.dir, taskID+".restore.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var journal restoreJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return nil, err
	}
	if journal.CheckpointID == "" {
		return nil, fmt.Errorf("restore journal for %s is missing checkpoint_id", taskID)
	}
	return &journal, nil
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
		var row restoreJournal
		if json.Unmarshal(raw, &row) != nil || row.CheckpointID == "" {
			return nil, fmt.Errorf("restore journal %s is corrupt", e.Name())
		}
		if row.State == "committed" {
			_ = os.Remove(p)
			continue
		}
		row.Version = restoreJournalVersion
		row.State = "blocked_reconciliation_required"
		row.Failure = "daemon restarted with an incomplete checkpoint restore"
		row.RecoveryAction = "retry session.checkpoint.restore with the same checkpoint_id"
		row.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
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
	Version            int         `json:"version,omitempty"`
	CheckpointID       string      `json:"checkpoint_id,omitempty"`
	ParentCheckpointID string      `json:"parent_checkpoint_id,omitempty"`
	CreatedAt          string      `json:"created_at,omitempty"`
	Sequence           int64       `json:"sequence,omitempty"`
	Turn               int         `json:"turn"`
	Transcript         *Transcript `json:"transcript"`
	MemorySnapshot     string      `json:"memory_snapshot,omitempty"`
	AppliedPatches     []string    `json:"applied_patches,omitempty"`
}

func (r *runStore) saveCheckpoint(taskID string, cp *runCheckpoint) {
	_ = r.saveCheckpointChecked(taskID, cp)
}

func (r *runStore) saveCheckpointChecked(taskID string, cp *runCheckpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cp == nil || cp.Transcript == nil {
		return fmt.Errorf("checkpoint transcript is required")
	}
	if cp.CheckpointID == "" {
		cp.Sequence = r.nextCheckpointSequenceLocked()
		cp.CreatedAt = time.Unix(0, cp.Sequence).UTC().Format(time.RFC3339Nano)
		if latest := readRunCheckpoint(filepath.Join(r.dir, taskID+".ckpt.json")); latest != nil {
			cp.ParentCheckpointID = runCheckpointID(taskID, latest)
		}
		cp.CheckpointID = fmt.Sprintf("%s:%d:%d", taskID, cp.Turn, cp.Sequence)
	}
	raw, err := encodeRunCheckpoint(cp)
	if err != nil {
		return err
	}
	historyDir := filepath.Join(r.dir, taskID+".ckpts")
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		return err
	}
	historyPath := filepath.Join(historyDir, fmt.Sprintf("%020d.json", cp.Sequence))
	if _, err := os.Stat(historyPath); err == nil {
		existing, readErr := os.ReadFile(historyPath)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(existing, raw) {
			return fmt.Errorf("checkpoint history entry conflicts with immutable checkpoint %s", cp.CheckpointID)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		historyTmp := historyPath + ".tmp"
		if err := os.WriteFile(historyTmp, raw, 0o600); err != nil {
			return err
		}
		if err := os.Rename(historyTmp, historyPath); err != nil {
			return err
		}
	}
	// Publish latest only after the immutable history entry is durable.
	p := filepath.Join(r.dir, taskID+".ckpt.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (r *runStore) nextCheckpointSequenceLocked() int64 {
	// The sequence doubles as a globally comparable creation instant. Keeping
	// it monotonic across every task survives wall-clock rollback after restart
	// via the initial history scan.
	next := time.Now().UTC().UnixNano()
	if r.lastCheckpointSequence == 0 {
		runEntries, _ := os.ReadDir(r.dir)
		for _, runEntry := range runEntries {
			if !runEntry.IsDir() || !strings.HasSuffix(runEntry.Name(), ".ckpts") {
				continue
			}
			dir := filepath.Join(r.dir, runEntry.Name())
			checkpointEntries, _ := os.ReadDir(dir)
			for _, entry := range checkpointEntries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				if cp := readRunCheckpoint(filepath.Join(dir, entry.Name())); cp != nil && cp.Sequence > r.lastCheckpointSequence {
					r.lastCheckpointSequence = cp.Sequence
				}
			}
		}
	}
	if next <= r.lastCheckpointSequence {
		next = r.lastCheckpointSequence + 1
	}
	r.lastCheckpointSequence = next
	return next
}

// publishCheckpointLatest atomically moves an existing historical checkpoint
// to the resumable latest pointer without rewriting its immutable history row.
func (r *runStore) publishCheckpointLatest(taskID string, cp *runCheckpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := encodeRunCheckpoint(cp)
	if err != nil {
		return err
	}
	p := filepath.Join(r.dir, taskID+".ckpt.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func encodeRunCheckpoint(cp *runCheckpoint) ([]byte, error) {
	if cp == nil || cp.Transcript == nil {
		return nil, fmt.Errorf("checkpoint transcript is required")
	}
	row := *cp
	row.Version = checkpointStoreVersion
	return json.Marshal(&row)
}

func (r *runStore) loadCheckpoint(taskID string) *runCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return readRunCheckpoint(filepath.Join(r.dir, taskID+".ckpt.json"))
}

func (r *runStore) loadCheckpointTurn(taskID string, turn int) *runCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	var selected *runCheckpoint
	for _, cp := range r.listCheckpointsLocked(taskID) {
		if cp.Turn == turn {
			selected = cp
		}
	}
	return selected
}

func (r *runStore) loadCheckpointID(taskID, checkpointID string) *runCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cp := range r.listCheckpointsLocked(taskID) {
		if runCheckpointID(taskID, cp) == checkpointID {
			return cp
		}
	}
	return nil
}

func (r *runStore) listCheckpoints(taskID string) []*runCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCheckpointsLocked(taskID)
}

func (r *runStore) listCheckpointsLocked(taskID string) []*runCheckpoint {
	dir := filepath.Join(r.dir, taskID+".ckpts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		latestPath := filepath.Join(r.dir, taskID+".ckpt.json")
		if cp := readRunCheckpoint(latestPath); cp != nil {
			if cp.CreatedAt == "" {
				if info, statErr := os.Stat(latestPath); statErr == nil {
					cp.CreatedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
				}
			}
			return []*runCheckpoint{cp}
		}
		return nil
	}
	out := make([]*runCheckpoint, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if cp := readRunCheckpoint(path); cp != nil {
			if cp.CreatedAt == "" {
				if info, err := entry.Info(); err == nil {
					cp.CreatedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
				}
			}
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		leftTime := checkpointCreatedAt(out[i])
		rightTime := checkpointCreatedAt(out[j])
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		if out[i].Sequence != out[j].Sequence {
			return out[i].Sequence < out[j].Sequence
		}
		if out[i].Turn != out[j].Turn {
			return out[i].Turn < out[j].Turn
		}
		return runCheckpointID(taskID, out[i]) < runCheckpointID(taskID, out[j])
	})
	return out
}

func runCheckpointID(taskID string, cp *runCheckpoint) string {
	if cp != nil && cp.CheckpointID != "" {
		return cp.CheckpointID
	}
	if cp == nil {
		return ""
	}
	return fmt.Sprintf("%s:%d", taskID, cp.Turn)
}

// readRunCheckpoint reads one checkpoint file. A checkpoint stamped by a
// newer binary is quarantined (never deleted) and nil is returned, so the
// resume path falls back to a fresh start instead of misreading a future
// format.
func readRunCheckpoint(path string) *runCheckpoint {
	raw, _, ok := statefmt.ReadVersioned(path, checkpointStoreVersion)
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
