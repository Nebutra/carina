package scheduler

import (
	"fmt"
	"time"

	"github.com/Nebutra/carina/go/continuity"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// Task is a delegated unit of background work leased to carina-worker. It is
// deliberately separate from ExecutionRun: worker delivery has lease and
// at-least-once semantics that do not belong to an interactive conversation.
type Task struct {
	TaskID                     string           `json:"task_id"`
	SessionID                  string           `json:"session_id"`
	WorkspaceID                string           `json:"workspace_id"`
	Status                     string           `json:"status"`
	Revision                   int64            `json:"revision,omitempty"`
	Continuity                 continuity.State `json:"continuity"`
	UserPrompt                 string           `json:"user_prompt"`
	Locale                     string           `json:"locale,omitempty"`
	SuccessCriteria            []SuccessCheck   `json:"success_criteria,omitempty"`
	CreatedAt                  time.Time        `json:"created_at"`
	UpdatedAt                  time.Time        `json:"updated_at"`
	Mode                       string           `json:"mode"`
	Summary                    string           `json:"summary,omitempty"`
	AppliedPatches             []string         `json:"applied_patches,omitempty"`
	TokensUsed                 int              `json:"tokens_used,omitempty"`
	TokenUsageObserved         bool             `json:"token_usage_observed,omitempty"`
	LeaseOwner                 string           `json:"lease_owner,omitempty"`
	LeaseExpiry                time.Time        `json:"lease_expiry,omitempty"`
	LeaseGeneration            int              `json:"lease_generation,omitempty"`
	Attempts                   int              `json:"attempts,omitempty"`
	RequiredWorkerCapabilities []string         `json:"required_worker_capabilities,omitempty"`
}

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
	task := s.PrepareDispatchTask(sessionID, workspaceID, prompt, criteria, required)
	if err := s.EnqueueDispatchTask(task); err != nil {
		panic(err)
	}
	return task
}

// PrepareDispatchTask builds a complete delegated-task envelope without
// making it leaseable. Callers that own routing metadata or durable bindings
// can install those prerequisites before EnqueueDispatchTask publishes the
// task to workers.
func (s *Scheduler) PrepareDispatchTask(sessionID, workspaceID, prompt string, criteria []SuccessCheck, required []string) *Task {
	now := time.Now().UTC()
	return &Task{
		TaskID:                     sessionstore.NewID("task"),
		SessionID:                  sessionID,
		WorkspaceID:                workspaceID,
		Status:                     "queued",
		Revision:                   1,
		Continuity:                 continuity.ForTaskStatus("queued", len(criteria) > 0),
		UserPrompt:                 prompt,
		SuccessCriteria:            criteria,
		Mode:                       "dispatch",
		RequiredWorkerCapabilities: append([]string(nil), required...),
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}

}

// EnqueueDispatchTask is the admission boundary for remote workers. The task
// must be fully initialized before this call; after it returns a worker may
// lease and report it immediately.
func (s *Scheduler) EnqueueDispatchTask(task *Task) error {
	if task == nil || task.TaskID == "" || task.Status != "queued" {
		return fmt.Errorf("scheduler: invalid prepared dispatch task")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[task.TaskID]; exists {
		return fmt.Errorf("scheduler: delegated task %s already exists", task.TaskID)
	}
	s.tasks[task.TaskID] = task
	s.dispatchQueue = append(s.dispatchQueue, task.TaskID)
	return nil
}

func (s *Scheduler) GetTask(taskID string) (*Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	return task, ok
}

func (s *Scheduler) SetTaskLocale(taskID, locale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.tasks[taskID]; task != nil {
		updated := *task
		updated.Locale = locale
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

func (s *Scheduler) CancelTask(taskID string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown delegated task %s", taskID)
	}
	if isTerminal(task.Status) {
		copy := *task
		return &copy, nil
	}
	updated := *task
	updated.Status = "cancelled"
	updated.LeaseOwner = ""
	updated.LeaseExpiry = time.Time{}
	updated.Continuity.Execution = continuity.ExecutionLease{}
	touchTask(&updated)
	s.tasks[taskID] = &updated
	return &updated, nil
}

func (s *Scheduler) LoadTask(task *Task) {
	if task == nil || task.TaskID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[task.TaskID]; exists {
		return
	}
	loaded := *task
	if loaded.Revision < 1 {
		loaded.Revision = 1
	}
	s.tasks[loaded.TaskID] = &loaded
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
		generation := updated.Continuity.Execution.LeaseGeneration + 1
		if generation < 1 {
			generation = 1
		}
		updated.LeaseGeneration = int(generation) // compatibility mirror
		updated.Continuity.Execution = continuity.ExecutionLease{
			OwnerKind: "remote", OwnerID: workerID, LeaseGeneration: generation, ExpiresAt: updated.LeaseExpiry,
		}
		touchTask(&updated)
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
	if int64(generation) != t.Continuity.Execution.LeaseGeneration {
		return fmt.Errorf("scheduler: task %s lease generation is stale", taskID)
	}
	updated := *t
	updated.LeaseExpiry = time.Now().UTC().Add(ttl)
	updated.Continuity.Execution.ExpiresAt = updated.LeaseExpiry
	touchTask(&updated)
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
	if int64(generation) != t.Continuity.Execution.LeaseGeneration {
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
	updated.Continuity.Execution.OwnerKind = ""
	updated.Continuity.Execution.OwnerID = ""
	updated.Continuity.Execution.ExpiresAt = time.Time{}
	touchTask(&updated)
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
			updated.Continuity.Execution.OwnerKind = ""
			updated.Continuity.Execution.OwnerID = ""
			updated.Continuity.Execution.ExpiresAt = time.Time{}
			touchTask(&updated)
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

func touchTask(task *Task) {
	if task.Revision < 1 {
		task.Revision = 1
	}
	if task.Continuity.Activity == "" {
		task.Continuity = continuity.ForTaskStatus(task.Status, len(task.SuccessCriteria) > 0)
	}
	task.Revision++
	task.UpdatedAt = time.Now().UTC()
	task.Continuity = continuity.MergeTaskStatus(task.Continuity, task.Status, len(task.SuccessCriteria) > 0)
}
