package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	sessionstore "github.com/Nebutra/carina/go/session-store"
	"github.com/Nebutra/carina/go/statefmt"
)

const runtimeStateVersion = 1

type runtimeState struct {
	Version      int       `json:"version"`
	Epoch        int64     `json:"epoch"`
	InstanceID   string    `json:"instance_id"`
	StartedAt    time.Time `json:"started_at"`
	ShutdownKind string    `json:"shutdown_kind,omitempty"`
	ShutdownAt   time.Time `json:"shutdown_at,omitempty"`
}

type runtimeLease struct {
	stateDir         string
	lock             *os.File
	state            runtimeState
	previousGraceful bool
	closed           bool
}

func acquireRuntimeLease(stateDir string) (*runtimeLease, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("runtime lease: create state dir: %w", err)
	}
	lockPath := filepath.Join(stateDir, "runtime.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("runtime lease: open lock: %w", err)
	}
	if err := acquireStateLock(lock); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("runtime lease: state directory is already owned: %w", err)
	}

	lease := &runtimeLease{stateDir: stateDir, lock: lock}
	previous := lease.readState()
	lease.previousGraceful = previous.ShutdownKind == "graceful"
	epoch := previous.Epoch + 1
	if epoch < 1 {
		epoch = 1
	}
	lease.state = runtimeState{
		Version: runtimeStateVersion, Epoch: epoch,
		InstanceID: sessionstore.NewID("runtime"), StartedAt: time.Now().UTC(),
	}
	if err := lease.persist(); err != nil {
		_ = releaseStateLock(lock)
		_ = lock.Close()
		return nil, err
	}
	return lease, nil
}

func (l *runtimeLease) readState() runtimeState {
	path := filepath.Join(l.stateDir, "runtime.json")
	raw, version, ok := statefmt.ReadVersioned(path, runtimeStateVersion)
	if !ok {
		return runtimeState{}
	}
	var state runtimeState
	if err := json.Unmarshal(raw, &state); err != nil {
		_ = statefmt.Quarantine(path, version)
		return runtimeState{}
	}
	return state
}

func (l *runtimeLease) persist() error {
	raw, err := json.MarshalIndent(l.state, "", "  ")
	if err != nil {
		return fmt.Errorf("runtime lease: encode state: %w", err)
	}
	raw = append(raw, '\n')
	path := filepath.Join(l.stateDir, "runtime.json")
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("runtime lease: open temp state: %w", err)
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("runtime lease: write state: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("runtime lease: close state: %w", closeErr)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("runtime lease: commit state: %w", err)
	}
	if dir, err := os.Open(l.stateDir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (l *runtimeLease) close(graceful bool) error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	var persistErr error
	if graceful {
		l.state.ShutdownKind = "graceful"
		l.state.ShutdownAt = time.Now().UTC()
		persistErr = l.persist()
	}
	unlockErr := releaseStateLock(l.lock)
	closeErr := l.lock.Close()
	if persistErr != nil {
		return persistErr
	}
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
