package scheduler

import (
	"sync"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/continuity"
)

func TestExecutionRunLineageAndLegacyNormalization(t *testing.T) {
	s := New()
	root := s.Submit("session-root", "workspace", "root")
	if root.ParentRunID != "" || root.RootRunID != root.RunID {
		t.Fatalf("root lineage = parent %q root %q", root.ParentRunID, root.RootRunID)
	}
	child, err := s.SubmitChildWithGoalModelAgent(root.RunID, "session-child", "workspace", "child", "", "build", nil)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := s.SubmitChildWithGoalModelAgent(child.RunID, "session-grandchild", "workspace", "grandchild", "", "explore", nil)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentRunID != root.RunID || child.RootRunID != root.RunID {
		t.Fatalf("child lineage = %+v", child)
	}
	if grandchild.ParentRunID != child.RunID || grandchild.RootRunID != root.RunID {
		t.Fatalf("grandchild lineage = %+v", grandchild)
	}
	if _, err := s.SubmitChildWithGoalModelAgent("run-missing", "session", "workspace", "no", "", "", nil); err == nil {
		t.Fatal("unknown parent accepted")
	}

	legacy := &ExecutionRun{
		RunID:       "run-legacy",
		ParentRunID: "run-old-parent",
		SessionID:   "session-legacy",
		WorkspaceID: "workspace",
		Status:      "completed",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	s.Load(legacy)
	normalized, ok := s.Get(legacy.RunID)
	if !ok || normalized.RootRunID != legacy.RunID || normalized.ParentRunID != legacy.ParentRunID {
		t.Fatalf("legacy normalization = %+v ok=%v", normalized, ok)
	}
}

func TestExecutionRunSnapshotsAreDeeplyImmutable(t *testing.T) {
	s := New()
	original := &ExecutionRun{
		RunID:           "run-loaded",
		RootRunID:       "run-loaded",
		SessionID:       "session",
		WorkspaceID:     "workspace",
		Status:          "interrupted",
		SuccessCriteria: []SuccessCheck{{Kind: "file_exists", Path: "proof.txt"}},
		InputMediaRefs:  []InputMediaRef{{ArtifactID: "artifact-1"}},
		AppliedPatches:  []string{"patch-1"},
		OutputSchema:    []byte(`{"type":"object"}`),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		Continuity: continuity.State{
			Activity: continuity.ActivityIdle,
			Outcome:  continuity.OutcomeInterrupted,
			Progress: continuity.ProgressInProgress,
			Recovery: continuity.RecoveryDecision{
				Disposition: continuity.RecoveryReviewRequired,
				Proofs:      map[string]bool{"checkpoint": true},
			},
			Interruption: &continuity.InterruptionRecord{Kind: continuity.InterruptionRuntimeLost},
			WorkspaceAnchor: &continuity.WorkspaceAnchor{
				ID: "anchor", WorkspaceRealpath: "/workspace", CreatedAt: time.Now().UTC(),
				DependencyFiles: []continuity.FileDigest{{Path: "proof.txt", SHA256: "abc"}},
				PatchLineage:    []string{"patch-1"},
			},
		},
	}
	s.Load(original)
	original.SuccessCriteria[0].Path = "mutated-original"
	original.Continuity.Recovery.Proofs["checkpoint"] = false
	original.Continuity.WorkspaceAnchor.PatchLineage[0] = "mutated-original"

	first, ok := s.Get(original.RunID)
	if !ok {
		t.Fatal("loaded run missing")
	}
	first.SuccessCriteria[0].Path = "mutated-snapshot"
	first.InputMediaRefs[0].ArtifactID = "mutated-snapshot"
	first.AppliedPatches[0] = "mutated-snapshot"
	first.OutputSchema[0] = 'X'
	first.Continuity.Recovery.Proofs["checkpoint"] = false
	first.Continuity.Interruption.Kind = continuity.InterruptionOperatorCancelled
	first.Continuity.WorkspaceAnchor.DependencyFiles[0].Path = "mutated-snapshot"
	first.Continuity.WorkspaceAnchor.PatchLineage[0] = "mutated-snapshot"

	second, _ := s.Get(original.RunID)
	if second.SuccessCriteria[0].Path != "proof.txt" || second.InputMediaRefs[0].ArtifactID != "artifact-1" ||
		second.AppliedPatches[0] != "patch-1" || string(second.OutputSchema) != `{"type":"object"}` ||
		!second.Continuity.Recovery.Proofs["checkpoint"] || second.Continuity.Interruption.Kind != continuity.InterruptionRuntimeLost ||
		second.Continuity.WorkspaceAnchor.DependencyFiles[0].Path != "proof.txt" || second.Continuity.WorkspaceAnchor.PatchLineage[0] != "patch-1" {
		t.Fatalf("scheduler state escaped through snapshot: %+v", second)
	}
	listed := s.List()
	listed[0].Summary = "mutated list"
	if current, _ := s.Get(original.RunID); current.Summary != "" {
		t.Fatalf("list exposed scheduler state: %+v", current)
	}
}

func TestRunUpdateSubscriptionFiltersAndCoalesces(t *testing.T) {
	s := New()
	target := s.Submit("session", "workspace", "target")
	other := s.Submit("session", "workspace", "other")
	updates, unsubscribe := s.SubscribeRunUpdates(target.RunID)

	s.SetStatus(other.RunID, "running")
	select {
	case update := <-updates:
		t.Fatalf("unrelated update delivered: %+v", update)
	default:
	}

	s.SetStatus(target.RunID, "running")
	s.SetStatus(target.RunID, "completed")
	select {
	case update := <-updates:
		if update.RunID != target.RunID {
			t.Fatalf("update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("run update was not delivered")
	}
	select {
	case update := <-updates:
		t.Fatalf("updates were not coalesced: %+v", update)
	default:
	}
	current, _ := s.Get(target.RunID)
	if current.Status != "completed" {
		t.Fatalf("coalesced re-read = %+v", current)
	}

	unsubscribe()
	s.mu.Lock()
	remaining := len(s.subscribers)
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("subscription leaked after unsubscribe: %d registered", remaining)
	}
	if _, ok := <-updates; ok {
		t.Fatal("subscription channel remained open after unsubscribe")
	}
}

func TestRunUpdateSubscriptionHasNoLostWakeup(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := New()
		task := s.Submit("session", "workspace", "race")
		updates, unsubscribe := s.SubscribeRunUpdates(task.RunID)
		done := make(chan struct{})
		go func() {
			s.SetStatus(task.RunID, "completed")
			close(done)
		}()
		current, _ := s.Get(task.RunID)
		if current.Status != "completed" {
			select {
			case <-updates:
			case <-time.After(time.Second):
				unsubscribe()
				t.Fatalf("iteration %d lost terminal update", i)
			}
		}
		<-done
		unsubscribe()
	}
}

func TestSchedulerSnapshotsRemainRaceFreeDuringUpdates(t *testing.T) {
	s := New()
	task := s.Submit("session", "workspace", "race snapshots")
	var writers sync.WaitGroup
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func(n int) {
			defer writers.Done()
			for j := 0; j < 100; j++ {
				s.SetResult(task.RunID, "summary", []string{"patch"})
				s.AddTokens(task.RunID, n+j)
				_, _ = s.Get(task.RunID)
				_ = s.List()
			}
		}(i)
	}
	writers.Wait()
}

func TestCancelIsIdempotentAndPreservesOtherTerminalOutcomes(t *testing.T) {
	s := New()
	cancellable := s.Submit("session", "workspace", "cancel")
	first, err := s.Cancel(cancellable.RunID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Cancel(cancellable.RunID)
	if err != nil || second.Status != "cancelled" || second.Revision != first.Revision {
		t.Fatalf("idempotent cancel = %+v err=%v, first=%+v", second, err, first)
	}

	completed := s.Submit("session", "workspace", "complete")
	s.SetStatus(completed.RunID, "completed")
	if current, err := s.Cancel(completed.RunID); err == nil || current.Status != "completed" {
		t.Fatalf("terminal cancel = %+v err=%v", current, err)
	}
	current, _ := s.Get(completed.RunID)
	if current.Status != "completed" {
		t.Fatalf("completed run rewritten as %s", current.Status)
	}
}
