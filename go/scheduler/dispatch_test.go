package scheduler

import (
	"testing"
	"time"
)

func leaseAny(s *Scheduler, workerID string, ttl time.Duration) (*Task, bool) {
	return s.LeaseMatching(workerID, ttl, func([]string) bool { return true })
}

func TestDispatchLeaseLifecycle(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	if s.DispatchDepth() != 1 {
		t.Fatalf("expected 1 queued, got %d", s.DispatchDepth())
	}

	// Lease claims it for worker A.
	leased, ok := leaseAny(s, "wrk_A", 5*time.Second)
	if !ok || leased.TaskID != task.TaskID {
		t.Fatalf("lease should return the queued task")
	}
	if leased.Status != "running" || leased.LeaseOwner != "wrk_A" || leased.Attempts != 1 || leased.LeaseGeneration != 1 {
		t.Fatalf("leased task not stamped: %+v", leased)
	}
	if s.DispatchDepth() != 0 {
		t.Fatalf("queue should be drained after lease, got %d", s.DispatchDepth())
	}

	// A second poll gets nothing.
	if _, ok := leaseAny(s, "wrk_B", 5*time.Second); ok {
		t.Fatal("empty queue must not yield a lease")
	}

	// Owner renews, then reports completion.
	if err := s.RenewLease(task.TaskID, "wrk_A", leased.LeaseGeneration, 5*time.Second); err != nil {
		t.Fatalf("owner renew failed: %v", err)
	}
	if err := s.Report(task.TaskID, "wrk_A", leased.LeaseGeneration, "completed", "done", []string{"patch_1"}); err != nil {
		t.Fatalf("report failed: %v", err)
	}
	got, _ := s.Get(task.TaskID)
	if got.Status != "completed" || got.Summary != "done" || got.LeaseOwner != "" {
		t.Fatalf("report did not finalize task: %+v", got)
	}
}

func TestLeaseMatchingPreservesUnsupportedTasks(t *testing.T) {
	s := New()
	guarded := s.SubmitForDispatchWithCapabilities("sess_1", "ws_1", "guarded", nil, []string{"process_tree_containment"})
	plain := s.SubmitForDispatchWithCapabilities("sess_1", "ws_1", "plain", nil, []string{"CommandExec"})

	leased, ok := s.LeaseMatching("wrk_plain", time.Second, func(required []string) bool { return len(required) == 1 && required[0] == "CommandExec" })
	if !ok || leased.TaskID != plain.TaskID {
		t.Fatalf("plain worker should skip guarded work and lease plain task: %+v", leased)
	}
	if queued, _ := s.Get(guarded.TaskID); queued.Status != "queued" {
		t.Fatalf("unsupported guarded task must remain queued: %+v", queued)
	}
	leased, ok = s.LeaseMatching("wrk_guarded", time.Second, func([]string) bool { return true })
	if !ok || leased.TaskID != guarded.TaskID {
		t.Fatalf("capable worker should later lease preserved guarded task: %+v", leased)
	}
}

func TestLegacyDispatchDoesNotInventProcessTreeContainment(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess", "ws", "work", nil)
	if len(task.RequiredWorkerCapabilities) != 0 {
		t.Fatalf("legacy dispatch unexpectedly requires capabilities: %+v", task.RequiredWorkerCapabilities)
	}
	if leased, ok := s.LeaseMatching("legacy", time.Second, func(required []string) bool { return len(required) == 0 }); !ok || leased == nil {
		t.Fatal("legacy worker could not lease unguarded dispatch task")
	}
}

func TestDispatchLeaseOwnershipEnforced(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	leased, ok := leaseAny(s, "wrk_A", 5*time.Second)
	if !ok {
		t.Fatal("lease failed")
	}
	// A non-owner cannot renew or report.
	if err := s.RenewLease(task.TaskID, "wrk_B", leased.LeaseGeneration, time.Second); err == nil {
		t.Fatal("non-owner renew must be rejected")
	}
	if err := s.Report(task.TaskID, "wrk_B", leased.LeaseGeneration, "completed", "hijack", nil); err == nil {
		t.Fatal("non-owner report must be rejected")
	}
}

func TestDispatchReapReQueuesExpiredLease(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	leased, _ := leaseAny(s, "wrk_A", 10*time.Millisecond)

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
	release, ok := leaseAny(s, "wrk_B", 5*time.Second)
	if !ok || release.Attempts != 2 {
		t.Fatalf("redelivery should bump attempts to 2, got %+v", release)
	}
	if err := s.Report(task.TaskID, "wrk_A", leased.LeaseGeneration, "failed", "stale", nil); err == nil {
		t.Fatal("stale worker (reaped) report must be rejected")
	}
	if err := s.Report(task.TaskID, "wrk_B", release.LeaseGeneration, "completed", "ok", nil); err != nil {
		t.Fatalf("new owner report failed: %v", err)
	}
}

func TestDispatchReportIsIdempotent(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	leased, _ := leaseAny(s, "wrk_A", 5*time.Second)
	if err := s.Report(task.TaskID, "wrk_A", leased.LeaseGeneration, "completed", "ok", nil); err != nil {
		t.Fatalf("first report failed: %v", err)
	}
	// A duplicate delivery of the same report is a safe no-op.
	if err := s.Report(task.TaskID, "wrk_A", leased.LeaseGeneration, "completed", "ok-again", nil); err != nil {
		t.Fatalf("duplicate report must be a no-op, got %v", err)
	}
	got, _ := s.Get(task.TaskID)
	if got.Summary != "ok" {
		t.Fatalf("duplicate report must not overwrite the result, got %q", got.Summary)
	}
}

func TestDispatchLeaseGenerationFencesSameWorkerRestart(t *testing.T) {
	s := New()
	task := s.SubmitForDispatch("sess_1", "ws_1", "do work", nil)
	first, _ := leaseAny(s, "wrk_A", 10*time.Millisecond)
	s.ReapExpiredLeases(first.LeaseExpiry.Add(time.Second))
	second, ok := leaseAny(s, "wrk_A", time.Second)
	if !ok || second.LeaseGeneration <= first.LeaseGeneration {
		t.Fatalf("lease generation did not advance: first=%d second=%d", first.LeaseGeneration, second.LeaseGeneration)
	}
	if err := s.RenewLease(task.TaskID, "wrk_A", first.LeaseGeneration, time.Second); err == nil {
		t.Fatal("stale generation from the same worker id must not renew")
	}
	if err := s.Report(task.TaskID, "wrk_A", first.LeaseGeneration, "completed", "stale", nil); err == nil {
		t.Fatal("stale generation from the same worker id must not report")
	}
	if err := s.Report(task.TaskID, "wrk_A", second.LeaseGeneration, "completed", "fresh", nil); err != nil {
		t.Fatalf("current generation report failed: %v", err)
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
	if _, ok := leaseAny(s, "wrk_A", time.Second); ok {
		t.Fatal("in-process task must not be leaseable via the dispatch queue")
	}
}
