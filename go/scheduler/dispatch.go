package scheduler

import (
	"fmt"
	"time"

	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// defaultLeaseTTL bounds how long a worker may hold a task without renewing
// before the scheduler assumes it crashed and re-queues the work.
const defaultLeaseTTL = 30 * time.Second

// SubmitForDispatch enqueues a task for remote execution via the work-dispatch
// bridge. Unlike Submit (which the local daemon runs in-process), a dispatched
// task waits on a dedicated queue until a remote worker leases it with Lease.
func (s *Scheduler) SubmitForDispatch(sessionID, workspaceID, prompt string, criteria []SuccessCheck) *Task {
	return s.SubmitForDispatchWithCapabilities(sessionID, workspaceID, prompt, criteria, nil)
}

func (s *Scheduler) SubmitForDispatchWithCapabilities(sessionID, workspaceID, prompt string, criteria []SuccessCheck, required []string) *Task {
	now := time.Now().UTC()
	task := &Task{
		TaskID:                     sessionstore.NewID("task"),
		SessionID:                  sessionID,
		WorkspaceID:                workspaceID,
		Status:                     "queued",
		UserPrompt:                 prompt,
		SuccessCriteria:            criteria,
		Mode:                       "dispatch",
		RequiredWorkerCapabilities: append([]string(nil), required...),
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}
	s.mu.Lock()
	s.tasks[task.TaskID] = task
	s.dispatchQueue = append(s.dispatchQueue, task.TaskID)
	s.mu.Unlock()
	return task
}

// Lease atomically claims the next queued dispatch task for a worker, marking it
// running with a lease that expires after ttl (the visibility timeout). Returns
// (nil, false) when nothing is queued. If the worker dies without reporting,
// ReapExpiredLeases re-queues the task once the lease lapses (at-least-once).
func (s *Scheduler) Lease(workerID string, ttl time.Duration) (*Task, bool) {
	// Without a capability matcher the scheduler has no evidence that the
	// worker satisfies a governed requirement, so only unguarded legacy tasks
	// are eligible.
	return s.LeaseMatching(workerID, ttl, func(required []string) bool { return len(required) == 0 })
}

func (s *Scheduler) LeaseMatching(workerID string, ttl time.Duration, supports func([]string) bool) (*Task, bool) {
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := len(s.dispatchQueue)
	for scanned := 0; scanned < queued && len(s.dispatchQueue) > 0; scanned++ {
		id := s.dispatchQueue[0]
		s.dispatchQueue = s.dispatchQueue[1:]
		t, ok := s.tasks[id]
		if !ok || t.Status != "queued" {
			continue // dropped or already claimed — skip stale queue entry
		}
		if supports != nil && !supports(t.RequiredWorkerCapabilities) {
			s.dispatchQueue = append(s.dispatchQueue, id)
			continue
		}
		now := time.Now().UTC()
		updated := *t
		updated.Status = "running"
		updated.LeaseOwner = workerID
		updated.LeaseExpiry = now.Add(ttl)
		updated.Attempts = t.Attempts + 1
		updated.LeaseGeneration = updated.Attempts
		updated.UpdatedAt = now
		s.tasks[id] = &updated
		return &updated, true
	}
	return nil, false
}

// RenewLease extends a held lease — the worker's heartbeat while it executes.
// Only the current lease owner may renew, and only while the task is running.
func (s *Scheduler) RenewLease(taskID, workerID string, generation int, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if t.Status != "running" {
		return fmt.Errorf("scheduler: task %s is %s, not leased", taskID, t.Status)
	}
	if t.LeaseOwner != workerID {
		return fmt.Errorf("scheduler: task %s is leased by another worker", taskID)
	}
	if generation != t.LeaseGeneration {
		return fmt.Errorf("scheduler: task %s lease generation is stale", taskID)
	}
	updated := *t
	updated.LeaseExpiry = time.Now().UTC().Add(ttl)
	updated.UpdatedAt = time.Now().UTC()
	s.tasks[taskID] = &updated
	return nil
}

// Report records a worker's terminal result for a leased task and clears the
// lease. It is idempotent: a duplicate report for an already-terminal task is a
// no-op, so at-least-once redelivery is safe. A report from a non-owner is
// rejected (a stale worker whose lease was reaped and reassigned cannot clobber
// the new owner's result).
func (s *Scheduler) Report(taskID, workerID string, generation int, status, summary string, patches []string) error {
	return s.ReportWithUsage(taskID, workerID, generation, status, summary, patches, 0, false)
}

// ReportWithUsage atomically records a terminal dispatch result and its
// optional executor-observed token spend. Keeping usage inside the fenced,
// idempotent report transaction prevents duplicate delivery from double-counting.
func (s *Scheduler) ReportWithUsage(taskID, workerID string, generation int, status, summary string, patches []string, tokensUsed int, usageObserved bool) error {
	if !isTerminal(status) {
		return fmt.Errorf("scheduler: %q is not a terminal status", status)
	}
	if tokensUsed < 0 {
		return fmt.Errorf("scheduler: tokens_used must be non-negative")
	}
	if !usageObserved && tokensUsed != 0 {
		return fmt.Errorf("scheduler: unobserved usage cannot report tokens")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if isTerminal(t.Status) {
		return nil // already reported — idempotent no-op
	}
	if t.LeaseOwner != workerID {
		return fmt.Errorf("scheduler: task %s is leased by another worker", taskID)
	}
	if generation != t.LeaseGeneration {
		return fmt.Errorf("scheduler: task %s lease generation is stale", taskID)
	}
	updated := *t
	updated.Status = status
	updated.Summary = summary
	updated.AppliedPatches = patches
	updated.TokensUsed = tokensUsed
	updated.TokenUsageObserved = usageObserved
	updated.LeaseOwner = ""
	updated.LeaseExpiry = time.Time{}
	updated.UpdatedAt = time.Now().UTC()
	s.tasks[taskID] = &updated
	return nil
}

// ReapExpiredLeases re-queues dispatch tasks whose lease expired (a worker
// crashed or stalled), returning the re-queued task ids. In-process tasks carry
// no lease owner and are never touched. This visibility-timeout recovery is what
// makes dispatch at-least-once.
func (s *Scheduler) ReapExpiredLeases(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var requeued []string
	for id, t := range s.tasks {
		if t.Status != "running" || t.LeaseOwner == "" || t.LeaseExpiry.IsZero() {
			continue
		}
		if now.After(t.LeaseExpiry) {
			updated := *t
			updated.Status = "queued"
			updated.LeaseOwner = ""
			updated.LeaseExpiry = time.Time{}
			updated.UpdatedAt = now
			s.tasks[id] = &updated
			s.dispatchQueue = append(s.dispatchQueue, id)
			requeued = append(requeued, id)
		}
	}
	return requeued
}

// DispatchDepth reports how many tasks are waiting for a worker (queue metric).
func (s *Scheduler) DispatchDepth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dispatchQueue)
}

func isTerminal(status string) bool {
	switch status {
	case "completed", "degraded", "failed", "cancelled":
		return true
	}
	return false
}
