// Package scheduler queues and tracks agent tasks (PRD §8.6).
// MVP: FIFO in-memory queue. Priorities, pause/resume, and multi-agent
// concurrency land in Phase 3.
package scheduler

import (
	"fmt"
	"sync"
	"time"

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

// Task mirrors protocol/schemas/task.schema.json.
type Task struct {
	TaskID          string         `json:"task_id"`
	SessionID       string         `json:"session_id"`
	WorkspaceID     string         `json:"workspace_id"`
	Status          string         `json:"status"` // queued | running | paused | waiting_approval | completed | degraded | failed | cancelled
	UserPrompt      string         `json:"user_prompt"`
	SuccessCriteria []SuccessCheck `json:"success_criteria,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	RiskLevel       int            `json:"risk_level"`
	Mode            string         `json:"mode,omitempty"`            // foreground | background
	Summary         string         `json:"summary,omitempty"`         // final result / degrade reason
	AppliedPatches  []string       `json:"applied_patches,omitempty"` // rollbackable patch ids
	TokensUsed      int            `json:"tokens_used,omitempty"`     // metered token spend (budget governance)
}

type Scheduler struct {
	mu    sync.Mutex
	queue []string
	tasks map[string]*Task
}

func New() *Scheduler {
	return &Scheduler{tasks: make(map[string]*Task)}
}

func (s *Scheduler) Submit(sessionID, workspaceID, prompt string) *Task {
	return s.SubmitWithGoal(sessionID, workspaceID, prompt, nil)
}

// SubmitWithGoal submits a task carrying objective success criteria.
func (s *Scheduler) SubmitWithGoal(sessionID, workspaceID, prompt string, criteria []SuccessCheck) *Task {
	now := time.Now().UTC()
	task := &Task{
		TaskID:          sessionstore.NewID("task"),
		SessionID:       sessionID,
		WorkspaceID:     workspaceID,
		Status:          "queued",
		UserPrompt:      prompt,
		SuccessCriteria: criteria,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
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

func (s *Scheduler) Cancel(taskID string) (*Task, error) {
	return s.transition(taskID, "cancelled")
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
			updated.UpdatedAt = time.Now().UTC()
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
	updated := *t
	updated.Status = status
	updated.UpdatedAt = time.Now().UTC()
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
	updated.UpdatedAt = time.Now().UTC()
	s.tasks[taskID] = &updated
}

// AddTokens accumulates metered token spend for budget governance.
func (s *Scheduler) AddTokens(taskID string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[taskID]; ok {
		updated := *t
		updated.TokensUsed += n
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

// Load reinserts a persisted task on daemon startup (run-registry recovery). It
// never clobbers a task already in memory.
func (s *Scheduler) Load(t *Task) {
	if t == nil || t.TaskID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[t.TaskID]; !exists {
		s.tasks[t.TaskID] = t
	}
}
