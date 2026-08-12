package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/statefmt"
)

const (
	backgroundIndexBatchSize = 16
	indexStateVersion        = 1
)

type indexBuildingError struct {
	progress indexBuildProgress
}

func (err *indexBuildingError) Error() string {
	return indexStatusFromProgress(err.progress)
}

type persistedIndexState struct {
	Version      int       `json:"version"`
	WorkspaceKey string    `json:"workspace_key"`
	Fingerprint  string    `json:"fingerprint"`
	Complete     bool      `json:"complete"`
	NextPath     int       `json:"next_path"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type backgroundIndexJob struct {
	mu            sync.RWMutex
	workspaceRoot string
	sessionID     string
	runID         string
	completed     int
	total         int
	state         string
}

type indexBuildProgress struct {
	Completed int
	Total     int
	State     string
}

func (job *backgroundIndexJob) progress() indexBuildProgress {
	job.mu.RLock()
	defer job.mu.RUnlock()
	return indexBuildProgress{
		Completed: job.completed,
		Total:     job.total,
		State:     job.state,
	}
}

func (job *backgroundIndexJob) update(completed, total int, state string) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.completed = completed
	job.total = total
	job.state = state
}

func snapshotFingerprint(snap *sweepSnapshot) string {
	paths := make([]string, 0, len(snap.stamps))
	for path := range snap.stamps {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		stamp := snap.stamps[path]
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n", filepath.ToSlash(path), stamp.mtime, stamp.size, uint32(stamp.mode))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func persistedStateFromSnapshot(root string, snap *sweepSnapshot, complete bool, next int) persistedIndexState {
	return persistedIndexState{
		Version:      indexStateVersion,
		WorkspaceKey: indexWorkspaceKey(root),
		Fingerprint:  snapshotFingerprint(snap),
		Complete:     complete,
		NextPath:     next,
		UpdatedAt:    time.Now().UTC(),
	}
}

func indexWorkspaceKey(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:])
}

func (d *Daemon) indexStatePath(root string) string {
	return filepath.Join(d.stateDir, "index-state", indexWorkspaceKey(root)+".json")
}

func (d *Daemon) loadIndexState(root string) (persistedIndexState, bool) {
	path := d.indexStatePath(root)
	data, _, ok := statefmt.ReadVersioned(path, indexStateVersion)
	if !ok {
		return persistedIndexState{}, false
	}
	var state persistedIndexState
	if json.Unmarshal(data, &state) != nil || state.Version != indexStateVersion || state.WorkspaceKey != indexWorkspaceKey(root) {
		statefmt.Quarantine(path, state.Version)
		return persistedIndexState{}, false
	}
	return state, true
}

func (d *Daemon) saveIndexState(state persistedIndexState) error {
	if decoded, err := hex.DecodeString(state.WorkspaceKey); err != nil || len(decoded) != sha256.Size {
		return errors.New("invalid index workspace key")
	}
	path := filepath.Join(d.stateDir, "index-state", state.WorkspaceKey+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".index-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// restoreReadyIndex makes an existing workspace index authoritative for this
// session only after the last completed source snapshot and the kernel database
// both agree that the current scanner view has a finished build. A legacy DB
// without this commit marker is reconciled once instead of being trusted from
// path names alone; equal paths do not prove equal content.
func (d *Daemon) restoreReadyIndex(sess *sessionstore.Session, snap *sweepSnapshot) (bool, error) {
	state, hasState := d.loadIndexState(sess.WorkspaceRoot)
	stateMatches := hasState && state.Complete && state.Fingerprint == snapshotFingerprint(snap)
	status, err := d.kern.IndexStatus(sess.SessionID)
	if err != nil {
		if strings.Contains(err.Error(), "index not built") {
			return false, nil
		}
		return false, err
	}
	if !status.Ready {
		return false, nil
	}
	if !stateMatches {
		return false, nil
	}
	d.indexBuilt.Store(sess.SessionID, true)
	d.indexSnapshot.Store(sess.SessionID, snap)
	return true, nil
}

func (d *Daemon) startBackgroundIndexBuild(sess *sessionstore.Session, task *scheduler.ExecutionRun, snap *sweepSnapshot) indexBuildProgress {
	root := filepath.Clean(sess.WorkspaceRoot)
	job := &backgroundIndexJob{
		workspaceRoot: root,
		sessionID:     sess.SessionID,
		runID:         task.RunID,
		total:         len(snap.stamps),
		state:         "building",
	}
	actual, loaded := d.indexJobs.LoadOrStore(root, job)
	if loaded {
		return actual.(*backgroundIndexJob).progress()
	}
	d.record(sess.SessionID, "ExecutionProgressed", task.RunID, "go", map[string]any{
		"status": "index_background_started", "indexed": 0, "total": len(snap.stamps),
	}, "")
	d.startBackgroundLoop(func() {
		defer d.indexJobs.Delete(root)
		d.runBackgroundIndexBuild(job, snap)
	})
	return job.progress()
}

func (d *Daemon) runBackgroundIndexBuild(job *backgroundIndexJob, initial *sweepSnapshot) {
	snap := initial
	for pass := 0; pass < 2; pass++ {
		stable, err := d.runBackgroundIndexPass(job, snap)
		if err != nil {
			job.update(job.progress().Completed, len(snap.stamps), "failed")
			d.record(job.sessionID, "ExecutionProgressed", job.runID, "go", map[string]any{
				"status": "index_background_failed", "reason": "kernel-error",
			}, "")
			return
		}
		if stable {
			job.update(len(snap.stamps), len(snap.stamps), "ready")
			d.indexBuilt.Store(job.sessionID, true)
			d.indexSnapshot.Store(job.sessionID, snap)
			d.record(job.sessionID, "ExecutionProgressed", job.runID, "go", map[string]any{
				"status": "index_background_completed", "indexed": len(snap.stamps), "total": len(snap.stamps),
			}, "")
			if err := d.syncEmbeddings(job.sessionID); err != nil {
				d.noteEmbeddingSyncFailure(job.sessionID, job.runID, err)
			}
			return
		}
		latest, err := d.scanSupportedStamps(job.workspaceRoot)
		if err != nil {
			job.update(job.progress().Completed, len(snap.stamps), "failed")
			return
		}
		snap = latest
		job.update(0, len(snap.stamps), "building")
	}
	job.update(job.progress().Completed, len(snap.stamps), "stale")
}

func (d *Daemon) runBackgroundIndexPass(job *backgroundIndexJob, snap *sweepSnapshot) (bool, error) {
	paths := make([]string, 0, len(snap.stamps))
	for path := range snap.stamps {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	fingerprint := snapshotFingerprint(snap)

	status, statusErr := d.kern.IndexStatus(job.sessionID)
	indexedBefore := []string{}
	if statusErr == nil {
		indexedBefore = status.IndexedPaths
	} else if !strings.Contains(statusErr.Error(), "index not built") {
		return false, statusErr
	}

	next := 0
	if state, ok := d.loadIndexState(job.workspaceRoot); ok && !state.Complete && state.Fingerprint == fingerprint && statusErr == nil {
		next = min(state.NextPath, len(paths))
	}
	batchSize := d.indexBuildBatchSize
	if batchSize <= 0 {
		batchSize = backgroundIndexBatchSize
	}
	for next < len(paths) {
		select {
		case <-d.stopCh:
			return false, errors.New("daemon stopping")
		default:
		}
		end := min(next+batchSize, len(paths))
		if _, err := d.kern.IndexUpdate(job.sessionID, paths[next:end], nil); err != nil {
			return false, err
		}
		next = end
		job.update(next, len(paths), "building")
		if err := d.saveIndexState(persistedStateFromSnapshot(job.workspaceRoot, snap, false, next)); err != nil {
			return false, err
		}
		// Give interactive kernel calls a scheduling window between bounded RPCs.
		select {
		case <-d.stopCh:
			return false, errors.New("daemon stopping")
		case <-time.After(10 * time.Millisecond):
		}
	}

	current := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		current[path] = struct{}{}
	}
	deleted := make([]string, 0)
	for _, path := range indexedBefore {
		if _, ok := current[path]; !ok {
			deleted = append(deleted, path)
		}
	}
	for from := 0; from < len(deleted); from += batchSize {
		end := min(from+batchSize, len(deleted))
		if _, err := d.kern.IndexUpdate(job.sessionID, nil, deleted[from:end]); err != nil {
			return false, err
		}
	}

	latest, err := d.scanSupportedStamps(job.workspaceRoot)
	if err != nil {
		return false, err
	}
	if snapshotFingerprint(latest) != fingerprint {
		return false, nil
	}
	if err := d.saveIndexState(persistedStateFromSnapshot(job.workspaceRoot, snap, true, len(paths))); err != nil {
		return false, err
	}
	return true, nil
}

func (d *Daemon) persistReadySnapshot(root string, snap *sweepSnapshot) error {
	return d.saveIndexState(persistedStateFromSnapshot(root, snap, true, len(snap.stamps)))
}

func (d *Daemon) updatePersistedSnapshot(sessionID string, changed []string) error {
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return nil
	}
	value, ok := d.indexSnapshot.Load(sessionID)
	if !ok {
		return nil
	}
	previous := value.(*sweepSnapshot)
	stamps := make(map[string]fileStamp, len(previous.stamps))
	for path, stamp := range previous.stamps {
		stamps[path] = stamp
	}
	for _, path := range changed {
		info, err := os.Stat(resolveIn(sess.WorkspaceRoot, path))
		if err != nil {
			delete(stamps, path)
			continue
		}
		stamps[path] = fileStamp{mtime: info.ModTime().UnixNano(), size: info.Size(), mode: info.Mode()}
	}
	snap := &sweepSnapshot{stamps: stamps, scannedAt: time.Now().UnixNano()}
	d.indexSnapshot.Store(sessionID, snap)
	return d.persistReadySnapshot(sess.WorkspaceRoot, snap)
}

func indexStatusFromProgress(progress indexBuildProgress) string {
	if progress.Total <= 0 {
		return "semantic index is starting in the background"
	}
	percent := progress.Completed * 100 / progress.Total
	return fmt.Sprintf("semantic index %s: %d/%d files (%d%%)", progress.State, progress.Completed, progress.Total, percent)
}

func renderProgressiveMap(progress indexBuildProgress, fallback string, semantic string) string {
	header := "Index coverage: " + indexStatusFromProgress(progress) + "."
	if strings.TrimSpace(semantic) != "" {
		return header + " This is a progressive high-entropy projection; retry later for complete coverage.\n" + semantic
	}
	return header + "\n" + fallback
}

func (d *Daemon) partialSemanticMap(sessionID string) string {
	raw, err := d.kern.IndexMap(sessionID, 1024)
	if err != nil {
		return ""
	}
	return renderSemanticRepoMap(raw, false)
}

func (d *Daemon) markIndexStateIncomplete(root string) {
	state, ok := d.loadIndexState(root)
	if !ok {
		return
	}
	state.Complete = false
	state.NextPath = 0
	state.UpdatedAt = time.Now().UTC()
	_ = d.saveIndexState(state)
}
