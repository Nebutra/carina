package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func projectionTestStore(t *testing.T) (*memoryProjectionStore, *time.Time) {
	t.Helper()
	s, err := newMemoryProjectionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.leaseTTL, s.baseBackoff, s.maxBackoff, s.maxAttempts = time.Minute, time.Second, 4*time.Second, 3
	return s, &now
}

func projectionScope() memoryScope {
	return memoryScope{Profile: "profile-a", WorkspaceRoot: "/workspace/a"}
}

func TestMemoryProjectionStableIdentityAndDesiredStateCoalescing(t *testing.T) {
	s, _ := projectionTestStore(t)
	first, err := s.Enqueue(projectionScope(), memoryTargetMemory, "entry-1", "alpha", false)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.Enqueue(projectionScope(), memoryTargetMemory, "entry-1", "alpha", false)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.DocumentID != first.DocumentID || duplicate.Generation != 1 {
		t.Fatalf("duplicate = %+v", duplicate)
	}
	second, err := s.Enqueue(projectionScope(), memoryTargetMemory, "entry-1", "beta", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.DocumentID != first.DocumentID || second.Generation != 2 || second.Revision == first.Revision {
		t.Fatalf("revision = %+v", second)
	}
	user, err := s.Enqueue(projectionScope(), memoryTargetUser, "entry-1", "beta", false)
	if err != nil {
		t.Fatal(err)
	}
	if user.DocumentID == second.DocumentID {
		t.Fatal("target boundaries must produce different document IDs")
	}
}

func TestMemoryProjectionStaleLeaseCannotCommitNewGeneration(t *testing.T) {
	s, _ := projectionTestStore(t)
	if _, err := s.Enqueue(projectionScope(), memoryTargetMemory, "entry", "v1", false); err != nil {
		t.Fatal(err)
	}
	claim, err := s.Claim()
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	newer, err := s.Enqueue(projectionScope(), memoryTargetMemory, "entry", "v2", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(*claim, nil); err == nil {
		t.Fatal("stale completion accepted")
	}
	current := s.state.Items[newer.DocumentID]
	if current.Generation != 2 || current.Status != projectionPending {
		t.Fatalf("current = %+v", current)
	}
}

func TestMemoryProjectionLeaseRecoverySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := newMemoryProjectionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.leaseTTL = time.Minute
	if _, err := s.Enqueue(projectionScope(), memoryTargetMemory, "entry", "v1", false); err != nil {
		t.Fatal(err)
	}
	first, err := s.Claim()
	if err != nil || first == nil {
		t.Fatalf("claim = %+v, %v", first, err)
	}
	reloaded, err := newMemoryProjectionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	reloaded.now = func() time.Time { return now }
	second, err := reloaded.Claim()
	if err != nil || second == nil {
		t.Fatalf("reclaim = %+v, %v", second, err)
	}
	if second.LeaseToken == first.LeaseToken || second.Attempts != 2 {
		t.Fatalf("recovered claim = %+v", second)
	}
	if got := reloaded.Status().LeaseRecoveries; got != 1 {
		t.Fatalf("recoveries = %d", got)
	}
}

func TestMemoryProjectionRetryBackoffAndBoundedFailure(t *testing.T) {
	s, now := projectionTestStore(t)
	if _, err := s.Enqueue(projectionScope(), memoryTargetMemory, "entry", "v1", false); err != nil {
		t.Fatal(err)
	}
	for attempt, delay := range []time.Duration{time.Second, 2 * time.Second} {
		claim, err := s.Claim()
		if err != nil || claim == nil {
			t.Fatalf("attempt %d claim = %+v, %v", attempt+1, claim, err)
		}
		if err := s.Complete(*claim, errors.New("temporary credentials in response must not be retained")); err != nil {
			t.Fatal(err)
		}
		item := s.state.Items[claim.DocumentID]
		if item.Status != projectionPending || item.NextAttemptAt.Sub(*now) != delay {
			t.Fatalf("attempt %d item = %+v", attempt+1, item)
		}
		*now = item.NextAttemptAt
	}
	claim, err := s.Claim()
	if err != nil || claim == nil {
		t.Fatalf("final claim = %+v, %v", claim, err)
	}
	if err := s.Complete(*claim, errors.New("still unavailable")); err != nil {
		t.Fatal(err)
	}
	if item := s.state.Items[claim.DocumentID]; item.Status != projectionFailed || item.Attempts != 3 {
		t.Fatalf("failed item = %+v", item)
	}
	if s.Status().Attempts != 3 || s.Status().Failed != 1 {
		t.Fatalf("status = %+v", s.Status())
	}
}

type recordingProjectionExecutor struct {
	puts, deletes int
	err           error
}

func (e *recordingProjectionExecutor) Put(context.Context, memoryProjectionIntent) error {
	e.puts++
	return e.err
}
func (e *recordingProjectionExecutor) Delete(context.Context, memoryProjectionIntent) error {
	e.deletes++
	return e.err
}

func TestMemoryProjectionTombstoneAndPermanentFailure(t *testing.T) {
	s, _ := projectionTestStore(t)
	intent, err := s.Enqueue(projectionScope(), memoryTargetMemory, "removed", "ignored", true)
	if err != nil {
		t.Fatal(err)
	}
	if !intent.Tombstone || intent.Content != "" {
		t.Fatalf("tombstone = %+v", intent)
	}
	executor := &recordingProjectionExecutor{err: permanentMemoryProjectionError(errors.New("remote rejected document"))}
	processed, err := s.ProcessOne(context.Background(), executor)
	if !processed || err == nil {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if executor.deletes != 1 || executor.puts != 0 {
		t.Fatalf("executor = %+v", executor)
	}
	if item := s.state.Items[intent.DocumentID]; item.Status != projectionFailed || item.Attempts != 1 {
		t.Fatalf("item = %+v", item)
	}
}

func TestMemoryProjectionAtomicPersistenceAndCorruptionFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := newMemoryProjectionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(projectionScope(), memoryTargetUser, "preference", "Use concise output", false); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newMemoryProjectionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Status(); got.Pending != 1 {
		t.Fatalf("status after reload = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "memory-projection", "outbox.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary file remains: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory-projection", "outbox.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newMemoryProjectionStore(dir); err == nil {
		t.Fatal("corrupt outbox was accepted")
	}
}

func TestMemoryProjectionDirtyWALMaterializesOnlyAfterAuthorization(t *testing.T) {
	dir := t.TempDir()
	s, err := newMemoryProjectionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	dirty, err := s.MarkDirty(projectionScope(), memoryTargetMemory, "carina_bank", "sess_test")
	if err != nil {
		t.Fatal(err)
	}
	if dirty.Status != projectionDirty || dirty.Content != "" {
		t.Fatalf("dirty WAL leaked content: %+v", dirty)
	}
	reloaded, err := newMemoryProjectionStore(dir)
	if err != nil || len(reloaded.Dirty()) != 1 {
		t.Fatalf("dirty WAL did not survive restart: %v %+v", err, reloaded.Dirty())
	}
	desired, err := reloaded.SetDesired(dirty.DocumentID, dirty.Generation, `{"version":1,"entries":["fact"]}`, false)
	if err != nil || desired.Status != projectionBlocked {
		t.Fatalf("desired=%+v err=%v", desired, err)
	}
	if claim, err := reloaded.Claim(); err != nil || claim != nil {
		t.Fatalf("unauthorized projection became executable: %+v %v", claim, err)
	}
	if err := reloaded.Authorize(desired.DocumentID, desired.Generation, "dec_externalize"); err != nil {
		t.Fatal(err)
	}
	if claim, err := reloaded.Claim(); err != nil || claim == nil || claim.BankID != "carina_bank" {
		t.Fatalf("authorized claim=%+v err=%v", claim, err)
	}
}

func TestMemoryProjectionDiscardDirtyOnRejectedLocalWrite(t *testing.T) {
	s, _ := projectionTestStore(t)
	dirty, err := s.MarkDirty(projectionScope(), memoryTargetUser, "bank", "sess_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardDirty(dirty.DocumentID, dirty.Generation); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(); got.Dirty != 0 || len(s.Dirty()) != 0 {
		t.Fatalf("discard failed: %+v", got)
	}
}
