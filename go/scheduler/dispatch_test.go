package scheduler

import (
	"testing"
	"time"
)

func TestDispatchLeaseLifecycle(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	if s.DispatchDepth() != 1 {
		t.Fatalf("expected 1 queued, got %d", s.DispatchDepth())
	}

	// Lease claims it for worker A.
	leased, ok := s.Lease("wrk_A", 5*time.Second)
	if !ok || leased.TaskID != task.TaskID {
		t.Fatalf("lease should return the queued task")
	}
	if leased.Status != "running" || leased.LeaseOwner != "wrk_A" || leased.Attempts != 1 {
		t.Fatalf("leased task not stamped: %+v", leased)
	}
	if s.DispatchDepth() != 0 {
		t.Fatalf("queue should be drained after lease, got %d", s.DispatchDepth())
	}

	// A second poll gets nothing.
	if _, ok := s.Lease("wrk_B", 5*time.Second); ok {
		t.Fatal("empty queue must not yield a lease")
	}

	// Owner renews, then reports completion.
	if err := s.RenewLease(task.TaskID, "wrk_A", 5*time.Second); err != nil {
		t.Fatalf("owner renew failed: %v", err)
	}
	if err := s.Report(task.TaskID, "wrk_A", "completed", "done", []string{"patch_1"}); err != nil {
		t.Fatalf("report failed: %v", err)
	}
	got, _ := s.Get(task.TaskID)
	if got.Status != "completed" || got.Summary != "done" || got.LeaseOwner != "" {
		t.Fatalf("report did not finalize task: %+v", got)
	}
}

func TestDispatchLeaseOwnershipEnforced(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	if _, ok := s.Lease("wrk_A", 5*time.Second); !ok {
		t.Fatal("lease failed")
	}
	// A non-owner cannot renew or report.
	if err := s.RenewLease(task.TaskID, "wrk_B", time.Second); err == nil {
		t.Fatal("non-owner renew must be rejected")
	}
	if err := s.Report(task.TaskID, "wrk_B", "completed", "hijack", nil); err == nil {
		t.Fatal("non-owner report must be rejected")
	}
}

func TestDispatchReapReQueuesExpiredLease(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	leased, _ := s.Lease("wrk_A", 10*time.Millisecond)

	// Before expiry: nothing reaped.
	if got := s.ReapExpiredLeases(leased.LeaseExpiry.Add(-time.Second)); len(got) != 0 {
		t.Fatalf("must not reap a live lease, got %v", got)
	}
	// After expiry: the task is re-queued for redelivery.
	requeued := s.ReapExpiredLeases(leased.LeaseExpiry.Add(time.Second))
	if len(requeued) != 1 || requeued[0] != task.TaskID {
		t.Fatalf("expired lease should be re-queued, got %v", requeued)
	}
	if s.DispatchDepth() != 1 {
		t.Fatalf("re-queued task should be pollable again, depth=%d", s.DispatchDepth())
	}

	// A new worker leases it (second attempt); the crashed worker's late report
	// must not clobber the reassignment.
	release, ok := s.Lease("wrk_B", 5*time.Second)
	if !ok || release.Attempts != 2 {
		t.Fatalf("redelivery should bump attempts to 2, got %+v", release)
	}
	if err := s.Report(task.TaskID, "wrk_A", "failed", "stale", nil); err == nil {
		t.Fatal("stale worker (reaped) report must be rejected")
	}
	if err := s.Report(task.TaskID, "wrk_B", "completed", "ok", nil); err != nil {
		t.Fatalf("new owner report failed: %v", err)
	}
}

func TestDispatchReportIsIdempotent(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	s.Lease("wrk_A", 5*time.Second)
	if err := s.Report(task.TaskID, "wrk_A", "completed", "ok", nil); err != nil {
		t.Fatalf("first report failed: %v", err)
	}
	// A duplicate delivery of the same report is a safe no-op.
	if err := s.Report(task.TaskID, "wrk_A", "completed", "ok-again", nil); err != nil {
		t.Fatalf("duplicate report must be a no-op, got %v", err)
	}
	got, _ := s.Get(task.TaskID)
	if got.Summary != "ok" {
		t.Fatalf("duplicate report must not overwrite the result, got %q", got.Summary)
	}
}

func TestDispatchIgnoresInProcessTasks(t *testing.T) {
	s := New()
	// A locally-run task (no lease) must never be reaped or leaseable.
	local := s.Submit("sess_1", "ws_1", "local work")
	s.SetStatus(local.TaskID, "running")
	if got := s.ReapExpiredLeases(time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("in-process task must not be reaped, got %v", got)
	}
	if _, ok := s.Lease("wrk_A", time.Second); ok {
		t.Fatal("in-process task must not be leaseable via the dispatch queue")
	}
}
