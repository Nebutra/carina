package scheduler

import "testing"

func TestSubmitAndGet(t *testing.T) {
	s := New()
	task := s.Submit("sess_1", "ws_1", "do a thing")
	if task.Status != "queued" {
		t.Fatalf("new task should be queued, got %s", task.Status)
	}
	got, ok := s.Get(task.TaskID)
	if !ok || got.UserPrompt != "do a thing" {
		t.Fatalf("Get returned %+v ok=%v", got, ok)
	}
	if _, ok := s.Get("task_missing"); ok {
		t.Fatal("Get of unknown task should be false")
	}
}

func TestNextPopsQueuedAndMarksRunning(t *testing.T) {
	s := New()
	a := s.Submit("s", "w", "a")
	b := s.Submit("s", "w", "b")
	first := s.Next()
	if first == nil || first.TaskID != a.TaskID || first.Status != "running" {
		t.Fatalf("Next should return the first task as running, got %+v", first)
	}
	second := s.Next()
	if second == nil || second.TaskID != b.TaskID {
		t.Fatalf("Next should return the second task, got %+v", second)
	}
	if s.Next() != nil {
		t.Fatal("Next on empty queue should be nil")
	}
}

func TestCancelAndStatuses(t *testing.T) {
	s := New()
	task := s.Submit("s", "w", "x")
	cancelled, err := s.Cancel(task.TaskID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel failed: %v %+v", err, cancelled)
	}
	if _, err := s.Cancel("task_missing"); err == nil {
		t.Fatal("cancel of unknown task should error")
	}
}

func TestCancelledTaskCannotBeRevived(t *testing.T) {
	s := New()
	task := s.Submit("s", "w", "cancel")
	if _, err := s.Cancel(task.TaskID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "completed", "failed", "degraded"} {
		s.SetStatus(task.TaskID, status)
	}
	got, _ := s.Get(task.TaskID)
	if got.Status != "cancelled" {
		t.Fatalf("cancelled task revived as %s", got.Status)
	}
}

func TestCountByStatusAndSetStatus(t *testing.T) {
	s := New()
	t1 := s.Submit("s", "w", "1")
	s.Submit("s", "w", "2")
	s.SetStatus(t1.TaskID, "completed")

	counts := s.CountByStatus()
	if counts["completed"] != 1 || counts["queued"] != 1 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
	if s.Count() != 2 {
		t.Fatalf("Count should be 2, got %d", s.Count())
	}
}

func TestCheckpointRestoreAndResumeTransitionsAreAtomic(t *testing.T) {
	s := New()
	task := s.Submit("s", "w", "restore")
	s.SetStatus(task.TaskID, "completed")

	restored, err := s.RestoreCheckpoint(task.TaskID, []string{"p1"})
	if err != nil || restored.Status != "paused" || len(restored.AppliedPatches) != 1 {
		t.Fatalf("restore transition = %+v, err=%v", restored, err)
	}
	running, err := s.Resume(task.TaskID)
	if err != nil || running.Status != "running" {
		t.Fatalf("resume transition = %+v, err=%v", running, err)
	}
	if _, err := s.Resume(task.TaskID); err == nil {
		t.Fatal("a running task must not be claimed by resume twice")
	}
}

func TestReconciliationRequiredBlocksResume(t *testing.T) {
	s := New()
	task := s.Submit("s", "w", "restore")
	s.SetStatus(task.TaskID, "paused")
	blocked, err := s.MarkReconciliationRequired(task.TaskID, "retry restore")
	if err != nil || !blocked.ReconciliationRequired || blocked.BlockedReason != "retry restore" {
		t.Fatalf("blocked transition = %+v, err=%v", blocked, err)
	}
	if _, err := s.Resume(task.TaskID); err == nil {
		t.Fatal("reconciliation-required task must not resume")
	}
}

func TestCancelledTaskRejectsRestoreAndReconciliation(t *testing.T) {
	s := New()
	task := s.Submit("s", "w", "cancel")
	if _, err := s.Cancel(task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreCheckpoint(task.TaskID, []string{"p1"}); err == nil {
		t.Fatal("cancelled task must reject checkpoint restore")
	}
	if _, err := s.MarkReconciliationRequired(task.TaskID, "blocked"); err == nil {
		t.Fatal("cancelled task must reject reconciliation transition")
	}
	current, _ := s.Get(task.TaskID)
	if current.Status != "cancelled" {
		t.Fatalf("cancelled task revived as %s", current.Status)
	}
}
