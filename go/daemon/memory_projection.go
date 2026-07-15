package daemon

import (
	"context"
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
)

const memoryProjectionVersion = 1

const (
	projectionDirty      = "dirty"
	projectionBlocked    = "blocked"
	projectionPending    = "pending"
	projectionProcessing = "processing"
	projectionCompleted  = "completed"
	projectionFailed     = "failed"
)

// memoryProjectionIntent is a durable desired state, not an event. Enqueuing a
// newer generation supersedes any in-flight work for the same document.
type memoryProjectionIntent struct {
	DocumentID              string      `json:"document_id"`
	BankID                  string      `json:"bank_id,omitempty"`
	SessionID               string      `json:"session_id,omitempty"`
	Scope                   memoryScope `json:"scope"`
	Target                  string      `json:"target"`
	Generation              uint64      `json:"generation"`
	Revision                string      `json:"revision"`
	Content                 string      `json:"content,omitempty"`
	Tombstone               bool        `json:"tombstone"`
	Status                  string      `json:"status"`
	Attempts                int         `json:"attempts"`
	NextAttemptAt           time.Time   `json:"next_attempt_at,omitempty"`
	LeaseToken              string      `json:"lease_token,omitempty"`
	LeaseExpiresAt          time.Time   `json:"lease_expires_at,omitempty"`
	LastError               string      `json:"last_error,omitempty"`
	DecisionID              string      `json:"decision_id,omitempty"`
	AuthorizationDecisionID string      `json:"authorization_decision_id,omitempty"`
	NetworkDecisionID       string      `json:"network_decision_id,omitempty"`
	CreatedAt               time.Time   `json:"created_at"`
	UpdatedAt               time.Time   `json:"updated_at"`
}

type memoryProjectionFile struct {
	Version         int                                `json:"version"`
	Items           map[string]*memoryProjectionIntent `json:"items"`
	LeaseRecoveries uint64                             `json:"lease_recoveries,omitempty"`
}

type memoryProjectionStatus struct {
	Dirty           int       `json:"dirty"`
	Blocked         int       `json:"blocked"`
	Pending         int       `json:"pending"`
	Processing      int       `json:"processing"`
	Completed       int       `json:"completed"`
	Failed          int       `json:"failed"`
	Attempts        uint64    `json:"attempts"`
	LeaseRecoveries uint64    `json:"lease_recoveries"`
	OldestPendingAt time.Time `json:"oldest_pending_at,omitempty"`
}

const memoryProjectionLogicalKey = "canonical-target-state-v1"

// memoryProjectionExecutor must make DocumentID+Generation idempotent remotely.
// Delete handles tombstones; Put replaces the complete remote document.
type memoryProjectionExecutor interface {
	Put(context.Context, memoryProjectionIntent) error
	Delete(context.Context, memoryProjectionIntent) error
}

type memoryProjectionPermanentError struct{ err error }

func (e memoryProjectionPermanentError) Error() string { return e.err.Error() }
func (e memoryProjectionPermanentError) Unwrap() error { return e.err }
func permanentMemoryProjectionError(err error) error {
	if err == nil {
		return nil
	}
	return memoryProjectionPermanentError{err: err}
}

type memoryProjectionStore struct {
	mu          sync.Mutex
	path        string
	now         func() time.Time
	leaseTTL    time.Duration
	baseBackoff time.Duration
	maxBackoff  time.Duration
	maxAttempts int
	state       memoryProjectionFile
}

func newMemoryProjectionStore(stateDir string) (*memoryProjectionStore, error) {
	s := &memoryProjectionStore{
		path: filepath.Join(stateDir, "memory-projection", "outbox.json"), now: time.Now,
		leaseTTL: 10 * time.Minute, baseBackoff: time.Second, maxBackoff: 5 * time.Minute, maxAttempts: 8,
		state: memoryProjectionFile{Version: memoryProjectionVersion, Items: map[string]*memoryProjectionIntent{}},
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// memoryProjectionDocumentID is stable across content revisions. logicalKey is
// a local entry identity, never the mutable memory text.
func memoryProjectionDocumentID(scope memoryScope, target, logicalKey string) (string, error) {
	target, err := normalizeMemoryTarget(target)
	if err != nil {
		return "", err
	}
	logicalKey = strings.TrimSpace(logicalKey)
	if logicalKey == "" {
		return "", errors.New("memory projection logical key is required")
	}
	identity := scope.Profile
	if target == memoryTargetMemory {
		identity += "\x00" + scope.WorkspaceRoot
	}
	sum := sha256.Sum256([]byte("carina-memory-document-v1\x00" + target + "\x00" + identity + "\x00" + logicalKey))
	return "mem_" + hex.EncodeToString(sum[:]), nil
}

func memoryProjectionRevision(content string, tombstone bool) string {
	kind := "put\x00"
	if tombstone {
		kind = "delete\x00"
	}
	sum := sha256.Sum256([]byte(kind + content))
	return hex.EncodeToString(sum[:])
}

func (s *memoryProjectionStore) Enqueue(scope memoryScope, target, logicalKey, content string, tombstone bool) (memoryProjectionIntent, error) {
	docID, err := memoryProjectionDocumentID(scope, target, logicalKey)
	if err != nil {
		return memoryProjectionIntent{}, err
	}
	target, _ = normalizeMemoryTarget(target)
	content = strings.TrimSpace(content)
	if !tombstone && content == "" {
		return memoryProjectionIntent{}, errors.New("memory projection content is required")
	}
	if tombstone {
		content = ""
	}
	revision := memoryProjectionRevision(content, tombstone)
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.state.Items[docID]; current != nil && current.Revision == revision {
		return *current, nil
	}
	now := s.now().UTC()
	generation := uint64(1)
	created := now
	if current := s.state.Items[docID]; current != nil {
		generation = current.Generation + 1
		created = current.CreatedAt
	}
	item := &memoryProjectionIntent{DocumentID: docID, Target: target, Generation: generation, Revision: revision,
		Content: content, Tombstone: tombstone, Status: projectionPending, CreatedAt: created, UpdatedAt: now, NextAttemptAt: now}
	s.state.Items[docID] = item
	if err := s.saveLocked(); err != nil {
		return memoryProjectionIntent{}, err
	}
	return *item, nil
}

// MarkDirty is the write-ahead boundary. It persists no memory content, but
// guarantees startup can rebuild desired state from the local authority after
// a crash between local commit and outbox materialization.
func (s *memoryProjectionStore) MarkDirty(scope memoryScope, target, bankID, sessionID string) (memoryProjectionIntent, error) {
	docID, err := memoryProjectionDocumentID(scope, target, memoryProjectionLogicalKey)
	if err != nil {
		return memoryProjectionIntent{}, err
	}
	target, _ = normalizeMemoryTarget(target)
	if strings.TrimSpace(bankID) == "" || strings.TrimSpace(sessionID) == "" {
		return memoryProjectionIntent{}, errors.New("memory projection bank and session are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	generation := uint64(1)
	created := now
	if current := s.state.Items[docID]; current != nil {
		generation, created = current.Generation+1, current.CreatedAt
	}
	item := &memoryProjectionIntent{DocumentID: docID, BankID: bankID, SessionID: sessionID, Scope: scope, Target: target, Generation: generation, Status: projectionDirty, CreatedAt: created, UpdatedAt: now}
	s.state.Items[docID] = item
	if err := s.saveLocked(); err != nil {
		return memoryProjectionIntent{}, err
	}
	return *item, nil
}

func (s *memoryProjectionStore) SetDesired(documentID string, generation uint64, content string, tombstone bool) (memoryProjectionIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[documentID]
	if item == nil || item.Generation != generation || item.Status != projectionDirty {
		return memoryProjectionIntent{}, errors.New("memory projection dirty generation is stale")
	}
	content = strings.TrimSpace(content)
	if !tombstone && content == "" {
		return memoryProjectionIntent{}, errors.New("memory projection content is required")
	}
	if tombstone {
		content = ""
	}
	item.Content, item.Tombstone, item.Revision = content, tombstone, memoryProjectionRevision(content, tombstone)
	item.Status, item.UpdatedAt, item.LastError = projectionBlocked, s.now().UTC(), "authorization_required"
	if err := s.saveLocked(); err != nil {
		return memoryProjectionIntent{}, err
	}
	return *item, nil
}

func (s *memoryProjectionStore) SetDecision(documentID string, generation uint64, decisionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[documentID]
	if item == nil || item.Generation != generation || item.Status != projectionBlocked {
		return errors.New("memory projection blocked generation is stale")
	}
	item.DecisionID, item.UpdatedAt = decisionID, s.now().UTC()
	return s.saveLocked()
}

func (s *memoryProjectionStore) SetBlockedReason(documentID string, generation uint64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[documentID]
	if item == nil || item.Generation != generation || item.Status != projectionBlocked {
		return errors.New("memory projection blocked generation is stale")
	}
	item.LastError, item.DecisionID, item.UpdatedAt = reason, "", s.now().UTC()
	return s.saveLocked()
}

func (s *memoryProjectionStore) DiscardDirty(documentID string, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[documentID]
	if item == nil || item.Generation != generation || item.Status != projectionDirty {
		return errors.New("memory projection dirty generation is stale")
	}
	delete(s.state.Items, documentID)
	return s.saveLocked()
}

func (s *memoryProjectionStore) SetNetworkDecision(documentID string, generation uint64, decisionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[documentID]
	if item == nil || item.Generation != generation || item.Status != projectionBlocked {
		return errors.New("memory projection blocked generation is stale")
	}
	old := *item
	item.NetworkDecisionID, item.UpdatedAt = decisionID, s.now().UTC()
	if err := s.saveLocked(); err != nil {
		*item = old
		return err
	}
	return nil
}

func (s *memoryProjectionStore) Authorize(documentID string, generation uint64, externalizeDecisionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[documentID]
	if item == nil || item.Generation != generation || item.Status != projectionBlocked {
		return errors.New("memory projection blocked generation is stale")
	}
	old := *item
	item.Status, item.AuthorizationDecisionID, item.DecisionID, item.LastError = projectionPending, externalizeDecisionID, "", ""
	item.NextAttemptAt, item.UpdatedAt = s.now().UTC(), s.now().UTC()
	if err := s.saveLocked(); err != nil {
		*item = old
		return err
	}
	return nil
}

func (s *memoryProjectionStore) RebindSession(documentID string, generation uint64, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[documentID]
	if item == nil || item.Generation != generation {
		return errors.New("memory projection generation is stale")
	}
	item.SessionID, item.UpdatedAt = sessionID, s.now().UTC()
	return s.saveLocked()
}

// Authorization is valid only for one daemon lifetime's endpoint and policy.
func (s *memoryProjectionStore) ReauthorizePending() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, item := range s.state.Items {
		if item.Status == projectionPending || item.Status == projectionProcessing || item.Status == projectionCompleted {
			item.Status, item.DecisionID, item.AuthorizationDecisionID = projectionBlocked, "", ""
			item.LeaseToken, item.LeaseExpiresAt = "", time.Time{}
			item.LastError, item.UpdatedAt = "restart_reauthorization_required", s.now().UTC()
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

func (s *memoryProjectionStore) Blocked(scope memoryScope) []memoryProjectionIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []memoryProjectionIntent
	for _, item := range s.state.Items {
		if item.Status == projectionBlocked && item.Scope.Profile == scope.Profile && item.Scope.WorkspaceRoot == scope.WorkspaceRoot {
			out = append(out, *item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DocumentID < out[j].DocumentID })
	return out
}

func (s *memoryProjectionStore) FailedToBlocked(scope memoryScope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, item := range s.state.Items {
		if item.Status == projectionFailed && item.Scope.Profile == scope.Profile && item.Scope.WorkspaceRoot == scope.WorkspaceRoot {
			item.Status, item.Attempts, item.DecisionID, item.AuthorizationDecisionID = projectionBlocked, 0, "", ""
			item.LastError, item.UpdatedAt = "manual_reauthorization_required", s.now().UTC()
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

func (s *memoryProjectionStore) Dirty() []memoryProjectionIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []memoryProjectionIntent
	for _, item := range s.state.Items {
		if item.Status == projectionDirty {
			out = append(out, *item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DocumentID < out[j].DocumentID })
	return out
}

func (s *memoryProjectionStore) Get(documentID string, generation uint64) (memoryProjectionIntent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[documentID]
	if item == nil || item.Generation != generation {
		return memoryProjectionIntent{}, false
	}
	return *item, true
}

func (s *memoryProjectionStore) Claim() (*memoryProjectionIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	changed := s.recoverExpiredLocked(now)
	var candidates []*memoryProjectionIntent
	for _, item := range s.state.Items {
		if item.Status == projectionPending && !item.NextAttemptAt.After(now) {
			candidates = append(candidates, item)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].NextAttemptAt.Equal(candidates[j].NextAttemptAt) {
			return candidates[i].DocumentID < candidates[j].DocumentID
		}
		return candidates[i].NextAttemptAt.Before(candidates[j].NextAttemptAt)
	})
	if len(candidates) == 0 {
		if changed {
			return nil, s.saveLocked()
		}
		return nil, nil
	}
	item := candidates[0]
	item.Status, item.Attempts, item.UpdatedAt = projectionProcessing, item.Attempts+1, now
	item.LeaseExpiresAt = now.Add(s.leaseTTL)
	tokenSeed := fmt.Sprintf("%s:%d:%d:%d", item.DocumentID, item.Generation, item.Attempts, now.UnixNano())
	token := sha256.Sum256([]byte(tokenSeed))
	item.LeaseToken = hex.EncodeToString(token[:16])
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	copy := *item
	return &copy, nil
}

func (s *memoryProjectionStore) Complete(claim memoryProjectionIntent, executeErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.state.Items[claim.DocumentID]
	if item == nil || item.Generation != claim.Generation || item.Status != projectionProcessing || item.LeaseToken != claim.LeaseToken {
		return errors.New("memory projection lease is stale")
	}
	now := s.now().UTC()
	item.LeaseToken, item.LeaseExpiresAt, item.UpdatedAt = "", time.Time{}, now
	if executeErr == nil {
		item.Status, item.LastError, item.NextAttemptAt = projectionCompleted, "", time.Time{}
	} else {
		var permanent memoryProjectionPermanentError
		isPermanent := errors.As(executeErr, &permanent)
		item.LastError = boundedProjectionError(isPermanent)
		if isPermanent || item.Attempts >= s.maxAttempts {
			item.Status, item.NextAttemptAt = projectionFailed, time.Time{}
		} else {
			item.Status = projectionPending
			item.NextAttemptAt = now.Add(s.retryDelay(item.Attempts))
		}
	}
	return s.saveLocked()
}

func (s *memoryProjectionStore) ProcessOne(ctx context.Context, executor memoryProjectionExecutor) (bool, error) {
	if executor == nil {
		return false, errors.New("memory projection executor is required")
	}
	claim, err := s.Claim()
	if err != nil || claim == nil {
		return false, err
	}
	if claim.Tombstone {
		err = executor.Delete(ctx, *claim)
	} else {
		err = executor.Put(ctx, *claim)
	}
	if completeErr := s.Complete(*claim, err); completeErr != nil {
		return true, completeErr
	}
	return true, err
}

func (s *memoryProjectionStore) Status() memoryProjectionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if s.recoverExpiredLocked(now) {
		_ = s.saveLocked()
	}
	status := memoryProjectionStatus{LeaseRecoveries: s.state.LeaseRecoveries}
	for _, item := range s.state.Items {
		status.Attempts += uint64(item.Attempts)
		switch item.Status {
		case projectionDirty:
			status.Dirty++
		case projectionBlocked:
			status.Blocked++
		case projectionPending:
			status.Pending++
			if status.OldestPendingAt.IsZero() || item.UpdatedAt.Before(status.OldestPendingAt) {
				status.OldestPendingAt = item.UpdatedAt
			}
		case projectionProcessing:
			status.Processing++
		case projectionCompleted:
			status.Completed++
		case projectionFailed:
			status.Failed++
		}
	}
	return status
}

func (s *memoryProjectionStore) recoverExpiredLocked(now time.Time) bool {
	changed := false
	for _, item := range s.state.Items {
		if item.Status == projectionProcessing && !item.LeaseExpiresAt.After(now) {
			item.Status, item.LeaseToken, item.LeaseExpiresAt = projectionPending, "", time.Time{}
			item.NextAttemptAt, item.UpdatedAt = now, now
			s.state.LeaseRecoveries++
			changed = true
		}
	}
	return changed
}

func (s *memoryProjectionStore) retryDelay(attempt int) time.Duration {
	d := s.baseBackoff
	for i := 1; i < attempt && d < s.maxBackoff; i++ {
		if d > s.maxBackoff/2 {
			return s.maxBackoff
		}
		d *= 2
	}
	if d > s.maxBackoff {
		return s.maxBackoff
	}
	return d
}

func boundedProjectionError(permanent bool) string {
	// Transport errors can contain authorization headers, URLs, or document
	// fragments. Persist only the retry classification; detailed errors belong
	// in the daemon's already-redacted transient diagnostics.
	if permanent {
		return "permanent"
	}
	return "retryable"
}

func (s *memoryProjectionStore) load() error {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state memoryProjectionFile
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("memory projection outbox corrupt: %w", err)
	}
	if state.Version != memoryProjectionVersion {
		return fmt.Errorf("memory projection outbox version %d is unsupported", state.Version)
	}
	if state.Items == nil {
		state.Items = map[string]*memoryProjectionIntent{}
	}
	s.state = state
	return nil
}

func (s *memoryProjectionStore) saveLocked() error {
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	defer os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	return closeErr
}
