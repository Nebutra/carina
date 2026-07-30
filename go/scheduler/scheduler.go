// Package scheduler queues and tracks agent runs (PRD §8.6).
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

// SuccessCheck is an objective completion criterion checked before accepting
// model-reported completion.
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

// ExecutionRun is the foreground conversation execution owned by the daemon.
type ExecutionRun struct {
	RunID                       string           `json:"run_id"`
	ClientSubmissionID          string           `json:"client_submission_id,omitempty"`
	ClientSubmissionFingerprint string           `json:"-"` // durable internal identity; never exposed through Task JSON
	SessionID                   string           `json:"session_id"`
	WorkspaceID                 string           `json:"workspace_id"`
	Status                      string           `json:"status"` // queued | running | paused | waiting_approval | interrupted | completed | degraded | failed | cancelled
	Revision                    int64            `json:"revision,omitempty"`
	Continuity                  continuity.State `json:"continuity"`
	UserPrompt                  string           `json:"user_prompt"`
	Locale                      string           `json:"locale,omitempty"`
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
}

type Scheduler struct {
	mu    sync.Mutex
	queue []string
	runs  map[string]*ExecutionRun
	// dispatchQueue holds delegated tasks awaiting a remote worker's lease.
	// It is separate from queue/the in-process path so the two never race for
	// the same unit of work.
	dispatchQueue []string
	tasks         map[string]*Task
}

func New() *Scheduler {
	return &Scheduler{runs: make(map[string]*ExecutionRun), tasks: make(map[string]*Task)}
}

func (s *Scheduler) Submit(sessionID, workspaceID, prompt string) *ExecutionRun {
	return s.SubmitWithGoal(sessionID, workspaceID, prompt, nil)
}

// SubmitWithGoal submits a task carrying objective success criteria.
func (s *Scheduler) SubmitWithGoal(sessionID, workspaceID, prompt string, criteria []SuccessCheck) *ExecutionRun {
	return s.SubmitWithGoalAndModel(sessionID, workspaceID, prompt, "", criteria)
}

// SubmitWithGoalAndModel submits a task with optional objective criteria and a
// model override such as "openai/gpt-5" or "openrouter/anthropic/claude...".
func (s *Scheduler) SubmitWithGoalAndModel(sessionID, workspaceID, prompt, model string, criteria []SuccessCheck) *ExecutionRun {
	return s.SubmitWithGoalModelAgent(sessionID, workspaceID, prompt, model, "", criteria)
}

func (s *Scheduler) SubmitWithGoalModelAgent(sessionID, workspaceID, prompt, model, agent string, criteria []SuccessCheck) *ExecutionRun {
	now := time.Now().UTC()
	task := &ExecutionRun{
		RunID:           sessionstore.NewID("run"),
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
	s.runs[task.RunID] = task
	s.queue = append(s.queue, task.RunID)
	s.mu.Unlock()
	return task
}

func (s *Scheduler) Get(taskID string) (*ExecutionRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.runs[taskID]
	return t, ok
}

func (s *Scheduler) SetClientSubmission(taskID, clientSubmissionID, fingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.runs[taskID]; task != nil {
		updated := *task
		updated.ClientSubmissionID = clientSubmissionID
		updated.ClientSubmissionFingerprint = fingerprint
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

func (s *Scheduler) SetInputMediaRefs(taskID string, refs []InputMediaRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.runs[taskID]; task != nil {
		updated := *task
		updated.InputMediaRefs = append([]InputMediaRef(nil), refs...)
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

func (s *Scheduler) SetModelState(taskID, requested, effective string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.runs[taskID]; task != nil {
		updated := *task
		updated.RequestedModel = requested
		updated.EffectiveModel = effective
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

func (s *Scheduler) SetReasoningEffortState(taskID, requested, effective string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.runs[taskID]; task != nil {
		updated := *task
		updated.RequestedReasoningEffort = requested
		updated.EffectiveReasoningEffort = effective
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

func (s *Scheduler) SetLocale(taskID, locale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.runs[taskID]; task != nil {
		updated := *task
		updated.Locale = locale
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

func (s *Scheduler) SetEffectiveModel(taskID, effective string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task := s.runs[taskID]; task != nil && effective != "" {
		updated := *task
		updated.EffectiveModel = effective
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

func (s *Scheduler) Cancel(taskID string) (*ExecutionRun, error) {
	cancelled, err := s.transition(taskID, "cancelled")
	if err == nil && cancelled != nil {
		s.mu.Lock()
		if current := s.runs[taskID]; current != nil {
			updated := *current
			updated.Continuity.Interruption = &continuity.InterruptionRecord{
				Kind: continuity.InterruptionOperatorCancelled, Actor: "user", ObservedAt: time.Now().UTC(),
				TaskID: taskID, Certainty: continuity.CertaintyObserved, Retryable: false,
				UserAction: "explicitly continue from a retained checkpoint or start a new task",
			}
			updated.Continuity.Recovery = continuity.RecoveryDecision{Disposition: continuity.RecoveryNone, Reason: "operator cancellation is never automatically recovered"}
			touchRun(&updated)
			s.runs[taskID] = &updated
			cancelled = &updated
		}
		s.mu.Unlock()
	}
	return cancelled, err
}

// Next pops the oldest queued task and marks it running.
// Returns nil when the queue is empty.
func (s *Scheduler) Next() *ExecutionRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) > 0 {
		id := s.queue[0]
		s.queue = s.queue[1:]
		if t, ok := s.runs[id]; ok && t.Status == "queued" {
			updated := *t
			updated.Status = "running"
			touchRun(&updated)
			s.runs[id] = &updated
			return &updated
		}
	}
	return nil
}

func (s *Scheduler) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

// CountByStatus returns the number of runs in each status (for metrics).
func (s *Scheduler) CountByStatus() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int)
	for _, t := range s.runs {
		out[t.Status]++
	}
	return out
}

// SetStatus transitions a task and is used by the in-daemon agent loop.
func (s *Scheduler) SetStatus(taskID, status string) {
	_, _ = s.transition(taskID, status)
}

func (s *Scheduler) transition(taskID, status string) (*ExecutionRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.runs[taskID]
	if !ok {
		return nil, fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	if t.Status == "cancelled" && status != "cancelled" {
		copy := *t
		return &copy, fmt.Errorf("scheduler: cancelled task %s is terminal", taskID)
	}
	updated := *t
	updated.Status = status
	touchRun(&updated)
	s.runs[taskID] = &updated
	return &updated, nil
}

// SetResult attaches a finished run's summary and applied-patch ids, so a
// completed/degraded background run is queryable without scanning the log.
func (s *Scheduler) SetResult(taskID, summary string, patches []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.runs[taskID]
	if !ok {
		return
	}
	updated := *t
	updated.Summary = summary
	updated.AppliedPatches = patches
	touchRun(&updated)
	s.runs[taskID] = &updated
}

func (s *Scheduler) SetAppliedPatches(taskID string, patches []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.runs[taskID]; ok {
		updated := *t
		updated.AppliedPatches = append([]string(nil), patches...)
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

// RestoreCheckpoint atomically moves a task to the paused checkpoint state.
// Keeping the patch lineage and lifecycle state in one scheduler mutation
// prevents observers from seeing a restored patch set paired with an old
// terminal status.
func (s *Scheduler) RestoreCheckpoint(taskID string, patches []string) (*ExecutionRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.runs[taskID]
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
	touchRun(&updated)
	s.runs[taskID] = &updated
	return &updated, nil
}

// MarkReconciliationRequired keeps a failed restore non-runnable until the
// same restore target is retried and committed successfully.
func (s *Scheduler) MarkReconciliationRequired(taskID, reason string, patches ...[]string) (*ExecutionRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.runs[taskID]
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
	touchRun(&updated)
	s.runs[taskID] = &updated
	return &updated, nil
}

// Resume atomically claims a paused task for execution. Callers must persist
// the returned running row before starting the agent loop.
func (s *Scheduler) Resume(taskID string) (*ExecutionRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.runs[taskID]
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
	touchRun(&updated)
	s.runs[taskID] = &updated
	return &updated, nil
}

// SetOutputSchema records the required keys the task's final JSON output must
// contain (structured output for headless/programmatic runs).
func (s *Scheduler) SetOutputSchema(taskID string, schema json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.runs[taskID]; ok {
		updated := *t
		updated.OutputSchema = append(json.RawMessage(nil), schema...)
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

// AddTokens accumulates metered token spend for budget governance.
func (s *Scheduler) AddTokens(taskID string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.runs[taskID]; ok {
		updated := *t
		updated.TokensUsed += n
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}
func (s *Scheduler) SetTokenBudget(taskID string, budget int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.runs[taskID]; ok {
		updated := *t
		updated.TokenBudget = budget
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

// SetMode records whether a task runs in the foreground or as a background run.
func (s *Scheduler) SetMode(taskID, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.runs[taskID]; ok {
		updated := *t
		updated.Mode = mode
		touchRun(&updated)
		s.runs[taskID] = &updated
	}
}

// List returns a snapshot of every task (the background-run registry).
func (s *Scheduler) List() []*ExecutionRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*ExecutionRun, 0, len(s.runs))
	for _, t := range s.runs {
		out = append(out, t)
	}
	return out
}

// Remove deletes a terminal task from the operator roster. Active work must be
// cancelled first so removing a row can never orphan execution.
func (s *Scheduler) Remove(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.runs[taskID]
	if !ok {
		return fmt.Errorf("scheduler: unknown task %s", taskID)
	}
	switch t.Status {
	case "completed", "failed", "cancelled", "degraded":
	default:
		return fmt.Errorf("scheduler: task %s is still %s", taskID, t.Status)
	}
	delete(s.runs, taskID)
	return nil
}

// Load reinserts a persisted task on daemon startup (run-registry recovery). It
// never clobbers a task already in memory.
func (s *Scheduler) Load(t *ExecutionRun) {
	if t == nil || t.RunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[t.RunID]; !exists {
		loaded := *t
		normalizeRun(&loaded)
		s.runs[t.RunID] = &loaded
	}
}

func normalizeRun(task *ExecutionRun) {
	if task.Revision < 1 {
		task.Revision = 1
	}
	if task.Continuity.Activity == "" {
		task.Continuity = continuity.ForTaskStatus(task.Status, len(task.SuccessCriteria) > 0)
	}
}

func touchRun(task *ExecutionRun) {
	normalizeRun(task)
	task.Revision++
	task.UpdatedAt = time.Now().UTC()
	task.Continuity = continuity.MergeTaskStatus(task.Continuity, task.Status, len(task.SuccessCriteria) > 0)
}
