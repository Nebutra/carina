package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/statefmt"
)

const compactJournalVersion = 1

type compactJournal struct {
	Version            int            `json:"version"`
	OperationID        string         `json:"operation_id"`
	SourceCheckpointID string         `json:"source_checkpoint_id"`
	Target             *runCheckpoint `json:"target"`
	State              string         `json:"state"`
	Failure            string         `json:"failure,omitempty"`
	UpdatedAt          string         `json:"updated_at"`
}

func durableAtomicWrite(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	return err
}

func (r *runStore) compactJournalPath(taskID string) string {
	return filepath.Join(r.dir, taskID+".compact.json")
}

func (r *runStore) writeCompactJournalLocked(taskID string, j *compactJournal) error {
	if j == nil || j.Target == nil {
		return fmt.Errorf("compact journal target is required")
	}
	j.Version = compactJournalVersion
	j.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return durableAtomicWrite(r.compactJournalPath(taskID), raw, 0600)
}

func (r *runStore) loadCompactJournal(taskID string) (*compactJournal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadCompactJournalLocked(taskID)
}
func (r *runStore) writeCompactJournal(taskID string, j *compactJournal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeCompactJournalLocked(taskID, j)
}
func (r *runStore) loadCompactJournalLocked(taskID string) (*compactJournal, error) {
	path := r.compactJournalPath(taskID)
	raw, version, ok := statefmt.ReadVersioned(path, compactJournalVersion)
	if !ok {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("compact journal unavailable")
	}
	var j compactJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		_ = statefmt.Quarantine(path, version)
		return nil, fmt.Errorf("corrupt compact journal: %w", err)
	}
	if j.Target == nil || j.OperationID == "" {
		return nil, fmt.Errorf("invalid compact journal")
	}
	return &j, nil
}

func (r *runStore) prepareCompact(taskID, operationID, sourceID string, target *runCheckpoint) (*compactJournal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, err := r.loadCompactJournalLocked(taskID); err != nil || existing != nil {
		return existing, err
	}
	if target == nil || target.Transcript == nil {
		return nil, fmt.Errorf("compact target transcript is required")
	}
	target.Sequence = r.nextCheckpointSequenceLocked()
	target.CreatedAt = time.Unix(0, target.Sequence).UTC().Format(time.RFC3339Nano)
	target.ParentCheckpointID = sourceID
	target.CheckpointID = fmt.Sprintf("%s:%d:%d", taskID, target.Turn, target.Sequence)
	j := &compactJournal{OperationID: operationID, SourceCheckpointID: sourceID, Target: target, State: "prepared"}
	if err := r.writeCompactJournalLocked(taskID, j); err != nil {
		return nil, err
	}
	return j, nil
}

func (r *runStore) commitCompact(taskID string, j *compactJournal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commitCompactLocked(taskID, j)
}
func (r *runStore) commitCompactLocked(taskID string, j *compactJournal) error {
	if j == nil || j.Target == nil {
		return fmt.Errorf("compact journal target is required")
	}
	raw, err := encodeRunCheckpoint(j.Target)
	if err != nil {
		return err
	}
	historyDir := filepath.Join(r.dir, taskID+".ckpts")
	historyPath := filepath.Join(historyDir, fmt.Sprintf("%020d.json", j.Target.Sequence))
	if existing, readErr := os.ReadFile(historyPath); readErr == nil {
		if string(existing) != string(raw) {
			return fmt.Errorf("compact target history conflict")
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	} else if err = durableAtomicWrite(historyPath, raw, 0600); err != nil {
		return err
	}
	j.State = "history_written"
	if err = r.writeCompactJournalLocked(taskID, j); err != nil {
		return err
	}
	if err = durableAtomicWrite(filepath.Join(r.dir, taskID+".ckpt.json"), raw, 0600); err != nil {
		return err
	}
	j.State = "committed"
	j.Failure = ""
	return r.writeCompactJournalLocked(taskID, j)
}
func (r *runStore) clearCompactJournal(taskID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := os.Remove(r.compactJournalPath(taskID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (r *runStore) recoverCompactJournals() {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, _ := os.ReadDir(r.dir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".compact.json") {
			continue
		}
		taskID := strings.TrimSuffix(entry.Name(), ".compact.json")
		j, err := r.loadCompactJournalLocked(taskID)
		if err != nil || j == nil {
			continue
		}
		if j.State != "committed" && j.State != "prepared" {
			if err = r.commitCompactLocked(taskID, j); err != nil {
				j.Failure = err.Error()
				_ = r.writeCompactJournalLocked(taskID, j)
			}
		}
	}
}
