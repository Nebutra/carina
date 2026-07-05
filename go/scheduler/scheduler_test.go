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
