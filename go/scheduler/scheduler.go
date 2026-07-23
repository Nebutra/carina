// Package scheduler queues and tracks agent tasks (PRD §8.6).
// MVP: FIFO in-memory queue. Priorities, pause/resume, and multi-agent
// concurrency land in Phase 3.
package scheduler

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/continuity"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// SuccessCheck is an objective completion criterion for a goal (Codex-style
// verifiable "done", instead of pure model self-judgment).
type SuccessCheck struct {
	Kind    string `json:"kind"` // command_zero_exit | file_exists | grep_absent
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}

type InputMediaRef struct {
	ArtifactID string `json:"artifact_id"`
	MediaType  string `json:"media_type"`
	Bytes      int64  `json:"bytes"`
	Origin     string `json:"origin,omitempty"`
}

// Task mirrors protocol/schemas/task.schema.json.
type Task struct {
	TaskID                      string           `json:"task_id"`
	ClientSubmissionID          string           `json:"client_submission_id,omitempty"`
	ClientSubmissionFingerprint string           `json:"-"` // durable internal identity; never exposed through Task JSON
	SessionID                   string           `json:"session_id"`
	WorkspaceID                 string           `json:"workspace_id"`
	Status                      string           `json:"status"` // queued | running | paused | waiting_approval | interrupted | completed | degraded | failed | cancelled
	Revision                    int64            `json:"revision,omitempty"`
	Continuity                  continuity.State `json:"continuity"`
	UserPrompt                  string           `json:"user_prompt"`
	InputMediaRefs              []InputMediaRef  `json:"input_media_refs,omitempty"`
	Model                       string           `json:"model,omitempty"` // provider/model override; empty => daemon default
	RequestedModel              string           `json:"requested_model,omitempty"`
	EffectiveModel              string           `json:"effective_model,omitempty"`
	RequestedReasoningEffort    string           `json:"requested_reasoning_effort,omitempty"`
	EffectiveReasoningEffort    string           `json:"effective_reasoning_effort,omitempty"`
	Agent                       string           `json:"agent,omitempty"` // agent mode/persona override; empty => build/default
	SuccessCriteria             []SuccessCheck   `json:"success_criteria,omitempty"`
	CreatedAt                   time.Time        `json:"created_at"`
	UpdatedAt                   time.Time        `json:"updated_at"`
	RiskLevel                   int              `json:"risk_level"`
	Mode                        string           `json:"mode,omitempty"`            // foreground | background
	Summary                     string           `json:"summary,omitempty"`         // final result / degrade reason
	AppliedPatches              []string         `json:"applied_patches,omitempty"` // rollbackable patch ids
	ReconciliationRequired      bool             `json:"reconciliation_required,omitempty"`
	BlockedReason               string           `json:"blocked_reason,omitempty"`
	TokensUsed                  int              `json:"tokens_used,omitempty"` // metered token spend (budget governance)
	TokenUsageObserved          bool             `json:"token_usage_observed,omitempty"`
	TokenBudget                 int              `json:"token_budget,omitempty"`
	OutputSchema                json.RawMessage  `json:"output_schema,omitempty"` // complete JSON Schema for final output
	// Work-dispatch lease (remote execution via the bridge). Empty for tasks the
	// local daemon runs in-process.
	LeaseOwner                 string    `json:"lease_owner,omitempty"`      // worker holding the dispatch lease
	LeaseExpiry                time.Time `json:"lease_expiry,omitempty"`     // visibility timeout; once past, the task is re-queued
	LeaseGeneration            int       `json:"lease_generation,omitempty"` // fencing token; changes on every successful lease
	Attempts                   int       `json:"attempts,omitempty"`         // dispatch delivery attempts (at-least-once)
	RequiredWorkerCapabilities []string  `json:"required_worker_capabilities,omitempty"`
}

type Scheduler struct {
	mu    sync.Mutex
	queue []string
	// dispatchQueue holds tasks awaiting a remote worker's lease (work.poll).
	// It is separate from queue/the in-process path so the two never race for
	// the same task.
	dispatchQueue []string
	tasks         map[string]*Task
}

func New() *Scheduler {
	return &Scheduler{tasks: make(map[string]*Task)}
}

func (s *Scheduler) Submit(sessionID, workspaceID, prompt string) *Task {
	return s.SubmitWithGoal(sessionID, workspaceID, prompt, nil)
}

// SubmitWithGoal submits a task carrying objective success criteria.
func (s *Scheduler) SubmitWithGoal(sessionID, workspaceID, prompt string, criteria []SuccessCheck) *Task {
	return s.SubmitWithGoalAndModel(sessionID, workspaceID, prompt, "", criteria)
}

// SubmitWithGoalAndModel submits a task with optional objective criteria and a
// model override such as "openai/gpt-5" or "openrouter/anthropic/claude...".
func (s *Scheduler) SubmitWithGoalAndModel(sessionID, workspaceID, prompt, model string, criteria []SuccessCheck) *Task {
	return s.SubmitWithGoalModelAgent(sessionID, workspaceID, prompt, model, "", criteria)
}

func (s *Scheduler) SubmitWithGoalModelAgent(sessionID, workspaceID, prompt, model, agent string, criteria []SuccessCheck) *Task {
	now := time.Now().UTC()
	task := &Task{
		TaskID:          sessionstore.NewID("task"),
		SessionID:       sessionID,
		WorkspaceID:     workspaceID,
		Status:          "queued",
		Revision:        1,
		Continuity:      continuity.EmptyState(),
		UserPrompt:      prompt,
		Model:           model,
		Agent:           agent,
		SuccessCriteria: criteria,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	task.Continuity.Progress = continuity.ProgressStarted
	s.mu.Lock()
	s.tasks[task.TaskID] = task
	s.queue = append(s.queue, task.TaskID)
	s.mu.Unlock()
	return task
}

func (s *Scheduler) Get(taskID string) (*Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	return t, ok
}

func (s *Scheduler) SetClientSubmission(taskID, clientSubmissionID, fingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.tasks[taskID]; task != nil {
		updated := *task
		updated.ClientSubmissionID = clientSubmissionID
		updated.ClientSubmissionFingerprint = fingerprint
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

func (s *Scheduler) SetInputMediaRefs(taskID string, refs []InputMediaRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.tasks[taskID]; task != nil {
		updated := *task
		updated.InputMediaRefs = append([]InputMediaRef(nil), refs...)
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

func (s *Scheduler) SetModelState(taskID, requested, effective string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.tasks[taskID]; task != nil {
		updated := *task
		updated.RequestedModel = requested
		updated.EffectiveModel = effective
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

func (s *Scheduler) SetReasoningEffortState(taskID, requested, effective string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.tasks[taskID]; task != nil {
		updated := *task
		updated.RequestedReasoningEffort = requested
		updated.EffectiveReasoningEffort = effective
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

func (s *Scheduler) SetEffectiveModel(taskID, effective string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.tasks[taskID]; task != nil && effective != "" {
		updated := *task
		updated.EffectiveModel = effective
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

func (s *Scheduler) Cancel(taskID string) (*Task, error) {
	cancelled, err := s.transition(taskID, "cancelled")
	if err == nil && cancelled != nil {
		s.mu.Lock()
		if current := s.tasks[taskID]; current != nil {
			updated := *current
			updated.Continuity.Interruption = &continuity.InterruptionRecord{
				Kind: continuity.InterruptionOperatorCancelled, Actor: "user", ObservedAt: time.Now().UTC(),
				TaskID: taskID, Certainty: continuity.CertaintyObserved, Retryable: false,
				UserAction: "explicitly continue from a retained checkpoint or start a new task",
			}
			updated.Continuity.Recovery = continuity.RecoveryDecision{Disposition: continuity.RecoveryNone, Reason: "operator cancellation is never automatically recovered"}
			touchTask(&updated)
			s.tasks[taskID] = &updated
			cancelled = &updated
		}
		s.mu.Unlock()
	}
	return cancelled, err
}

// Next pops the oldest queued task and marks it running.
// Returns nil when the queue is empty.
func (s *Scheduler) Next() *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) > 0 {
		id := s.queue[0]
		s.queue = s.queue[1:]
		if t, ok := s.tasks[id]; ok && t.Status == "queued" {
			updated := *t
			updated.Status = "running"
			touchTask(&updated)
			s.tasks[id] = &updated
			return &updated
		}
	}
	return nil
}

func (s *Scheduler) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks)
}

// CountByStatus returns the number of tasks in each status (for metrics).
func (s *Scheduler) CountByStatus() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int)
	for _, t := range s.tasks {
		out[t.Status]++
	}
	return out
}

// SetStatus transitions a task and is used by the in-daemon agent loop.
func (s *Scheduler) SetStatus(taskID, status string) {
	_, _ = s.transition(taskID, status)
}

func (s *Scheduler) transition(taskID, status string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if t.Status == "cancelled" && status != "cancelled" {
		copy := *t
		return &copy, fmt.Errorf("scheduler: cancelled task %s is terminal", taskID)
	}
	updated := *t
	updated.Status = status
	touchTask(&updated)
	s.tasks[taskID] = &updated
	return &updated, nil
}

// SetResult attaches a finished run's summary and applied-patch ids, so a
// completed/degraded background run is queryable without scanning the log.
func (s *Scheduler) SetResult(taskID, summary string, patches []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return
	}
	updated := *t
	updated.Summary = summary
	updated.AppliedPatches = patches
	touchTask(&updated)
	s.tasks[taskID] = &updated
}

func (s *Scheduler) SetAppliedPatches(taskID string, patches []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		updated := *t
		updated.AppliedPatches = append([]string(nil), patches...)
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

// RestoreCheckpoint atomically moves a task to the paused checkpoint state.
// Keeping the patch lineage and lifecycle state in one scheduler mutation
// prevents observers from seeing a restored patch set paired with an old
// terminal status.
func (s *Scheduler) RestoreCheckpoint(taskID string, patches []string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if t.Status == "cancelled" {
		return nil, fmt.Errorf("scheduler: cancelled task %s is terminal", taskID)
	}
	updated := *t
	updated.Status = "paused"
	updated.AppliedPatches = append([]string(nil), patches...)
	updated.ReconciliationRequired = false
	updated.BlockedReason = ""
	touchTask(&updated)
	s.tasks[taskID] = &updated
	return &updated, nil
}

// MarkReconciliationRequired keeps a failed restore non-runnable until the
// same restore target is retried and committed successfully.
func (s *Scheduler) MarkReconciliationRequired(taskID, reason string, patches ...[]string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if t.Status == "cancelled" {
		return nil, fmt.Errorf("scheduler: cancelled task %s is terminal", taskID)
	}
	updated := *t
	updated.Status = "paused"
	updated.ReconciliationRequired = true
	updated.BlockedReason = reason
	if len(patches) > 0 {
		updated.AppliedPatches = append([]string(nil), patches[0]...)
	}
	touchTask(&updated)
	s.tasks[taskID] = &updated
	return &updated, nil
}

// Resume atomically claims a paused task for execution. Callers must persist
// the returned running row before starting the agent loop.
func (s *Scheduler) Resume(taskID string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if t.Status != "paused" {
		return nil, fmt.Errorf("scheduler: task %s is %s, not paused", taskID, t.Status)
	}
	if t.ReconciliationRequired {
		return nil, fmt.Errorf("scheduler: task %s requires checkpoint reconciliation: %s", taskID, t.BlockedReason)
	}
	updated := *t
	updated.Status = "running"
	touchTask(&updated)
	s.tasks[taskID] = &updated
	return &updated, nil
}

// SetOutputSchema records the required keys the task's final JSON output must
// contain (structured output for headless/programmatic runs).
func (s *Scheduler) SetOutputSchema(taskID string, schema json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		updated := *t
		updated.OutputSchema = append(json.RawMessage(nil), schema...)
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

// AddTokens accumulates metered token spend for budget governance.
func (s *Scheduler) AddTokens(taskID string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		updated := *t
		updated.TokensUsed += n
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}
func (s *Scheduler) SetTokenBudget(taskID string, budget int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		updated := *t
		updated.TokenBudget = budget
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

// SetMode records whether a task runs in the foreground or as a background run.
func (s *Scheduler) SetMode(taskID, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		updated := *t
		updated.Mode = mode
		touchTask(&updated)
		s.tasks[taskID] = &updated
	}
}

// List returns a snapshot of every task (the background-run registry).
func (s *Scheduler) List() []*Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	return out
}

// Remove deletes a terminal task from the operator roster. Active work must be
// cancelled first so removing a row can never orphan execution.
func (s *Scheduler) Remove(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	switch t.Status {
	case "completed", "failed", "cancelled", "degraded":
	default:
		return fmt.Errorf("scheduler: task %s is still %s", taskID, t.Status)
	}
	delete(s.tasks, taskID)
	return nil
}

// Load reinserts a persisted task on daemon startup (run-registry recovery). It
// never clobbers a task already in memory.
func (s *Scheduler) Load(t *Task) {
	if t == nil || t.TaskID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[t.TaskID]; !exists {
		loaded := *t
		normalizeTask(&loaded)
		s.tasks[t.TaskID] = &loaded
	}
}

func normalizeTask(task *Task) {
	if task.Revision < 1 {
		task.Revision = 1
	}
	if task.Continuity.Activity == "" {
		task.Continuity = continuity.ForTaskStatus(task.Status, len(task.SuccessCriteria) > 0)
	}
	if task.Continuity.Execution.LeaseGeneration == 0 && task.LeaseGeneration > 0 {
		task.Continuity.Execution.LeaseGeneration = int64(task.LeaseGeneration)
		task.Continuity.Execution.OwnerKind = "remote"
		task.Continuity.Execution.OwnerID = task.LeaseOwner
		task.Continuity.Execution.ExpiresAt = task.LeaseExpiry
	}
}

func touchTask(task *Task) {
	normalizeTask(task)
	task.Revision++
	task.UpdatedAt = time.Now().UTC()
	task.Continuity = continuity.MergeTaskStatus(task.Continuity, task.Status, len(task.SuccessCriteria) > 0)
}
