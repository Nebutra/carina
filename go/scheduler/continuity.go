package scheduler

import (
	"fmt"
	"time"

	"github.com/Nebutra/carina/go/continuity"
)

// AcquireExecution atomically fences all prior owners and publishes a new
// execution generation. expectedRevision prevents recovery from acting on a
// stale continuity snapshot.
func (s *Scheduler) AcquireExecution(taskID string, expectedRevision int64, ownerKind, ownerID string, runtimeEpoch int64, expiresAt time.Time) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	normalizeTask(task)
	if expectedRevision > 0 && task.Revision != expectedRevision {
		return nil, fmt.Errorf("scheduler: task %s revision is stale", taskID)
	}
	if task.Status == "cancelled" || isTerminal(task.Status) {
		return nil, fmt.Errorf("scheduler: task %s is %s, not executable", taskID, task.Status)
	}
	if ownerKind == "" || ownerID == "" || (ownerKind == "local" && runtimeEpoch < 1) {
		return nil, fmt.Errorf("scheduler: invalid execution owner")
	}
	updated := *task
	if updated.Status == "interrupted" {
		if updated.Continuity.Recovery.Disposition != continuity.RecoveryResumeCheckpoint {
			return nil, fmt.Errorf("scheduler: task %s is not approved for automatic checkpoint recovery", taskID)
		}
		for proof, passed := range updated.Continuity.Recovery.Proofs {
			if !passed {
				return nil, fmt.Errorf("scheduler: task %s recovery proof %s failed", taskID, proof)
			}
		}
		if len(updated.Continuity.Recovery.Proofs) < 4 {
			return nil, fmt.Errorf("scheduler: task %s recovery proof set is incomplete", taskID)
		}
		if updated.Continuity.AutoRecoveryAttempts >= int(updated.Continuity.RecoveryGeneration) {
			return nil, fmt.Errorf("scheduler: task %s automatic recovery already attempted for generation %d", taskID, updated.Continuity.RecoveryGeneration)
		}
		updated.Continuity.AutoRecoveryAttempts = int(updated.Continuity.RecoveryGeneration)
	}
	generation := updated.Continuity.Execution.LeaseGeneration + 1
	if generation < 1 {
		generation = 1
	}
	updated.Status = "running"
	updated.Continuity.Execution = continuity.ExecutionLease{
		OwnerKind: ownerKind, OwnerID: ownerID, RuntimeEpoch: runtimeEpoch,
		LeaseGeneration: generation, ExpiresAt: expiresAt,
	}
	touchTask(&updated)
	s.tasks[taskID] = &updated
	copy := updated
	return &copy, nil
}

// SetStatusFenced publishes state only for the currently authoritative
// execution generation. It is the commit primitive used by recovered work.
func (s *Scheduler) SetStatusFenced(taskID string, generation int64, status string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if generation < 1 || task.Continuity.Execution.LeaseGeneration != generation {
		return nil, fmt.Errorf("scheduler: task %s execution generation is stale", taskID)
	}
	if task.Status == "cancelled" && status != "cancelled" {
		return nil, fmt.Errorf("scheduler: cancelled task %s is terminal", taskID)
	}
	updated := *task
	updated.Status = status
	if isTerminal(status) || status == "interrupted" || status == "paused" || status == "waiting_input" || status == "waiting_approval" {
		updated.Continuity.Execution.OwnerKind = ""
		updated.Continuity.Execution.OwnerID = ""
		updated.Continuity.Execution.ExpiresAt = time.Time{}
	}
	touchTask(&updated)
	s.tasks[taskID] = &updated
	copy := updated
	return &copy, nil
}

// SetTerminalResultFenced commits terminal status and its user-visible result
// in one mutation so observers never see a terminal row with stale summary.
func (s *Scheduler) SetTerminalResultFenced(taskID string, generation int64, status, summary string, patches []string) (*Task, error) {
	if !isTerminal(status) {
		return nil, fmt.Errorf("scheduler: %q is not terminal", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if (generation > 0 || task.Continuity.Execution.LeaseGeneration > 0) && task.Continuity.Execution.LeaseGeneration != generation {
		return nil, fmt.Errorf("scheduler: task %s execution generation is stale", taskID)
	}
	if task.Status == "cancelled" && status != "cancelled" {
		return nil, fmt.Errorf("scheduler: cancelled task %s is terminal", taskID)
	}
	updated := *task
	updated.Status, updated.Summary = status, summary
	updated.AppliedPatches = append([]string(nil), patches...)
	updated.Continuity.Execution.OwnerKind = ""
	updated.Continuity.Execution.OwnerID = ""
	updated.Continuity.Execution.ExpiresAt = time.Time{}
	touchTask(&updated)
	s.tasks[taskID] = &updated
	copy := updated
	return &copy, nil
}

// Interrupt abandons an old generation and records structured evidence. A
// cancelled task is never converted back into recoverable work.
func (s *Scheduler) Interrupt(taskID string, record continuity.InterruptionRecord, decision continuity.RecoveryDecision) (*Task, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if err := decision.Validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if task.Status == "cancelled" || isTerminal(task.Status) {
		copy := *task
		return &copy, nil
	}
	updated := *task
	updated.Status = "interrupted"
	updated.Continuity.Interruption = &record
	updated.Continuity.RecoveryGeneration++
	decision.RecoveryGeneration = updated.Continuity.RecoveryGeneration
	updated.Continuity.Recovery = decision
	updated.Continuity.Execution.OwnerKind = ""
	updated.Continuity.Execution.OwnerID = ""
	updated.Continuity.Execution.ExpiresAt = time.Time{}
	touchTask(&updated)
	s.tasks[taskID] = &updated
	copy := updated
	return &copy, nil
}

func (s *Scheduler) SetWorkspaceAnchor(taskID string, anchor continuity.WorkspaceAnchor) (*Task, error) {
	if err := anchor.Validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	updated := *task
	updated.Continuity.WorkspaceAnchor = &anchor
	touchTask(&updated)
	s.tasks[taskID] = &updated
	copy := updated
	return &copy, nil
}
