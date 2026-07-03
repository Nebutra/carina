// Package scheduler queues and tracks agent tasks (PRD §8.6).
// MVP: FIFO in-memory queue. Priorities, pause/resume, and multi-agent
// concurrency land in Phase 3.
package scheduler

import (
	"fmt"
	"sync"
	"time"

	sessionstore "github.com/TsekaLuk/pi-os/go/session-store"
)

// Task mirrors protocol/schemas/task.schema.json.
type Task struct {
	TaskID      string    `json:"task_id"`
	SessionID   string    `json:"session_id"`
	WorkspaceID string    `json:"workspace_id"`
	Status      string    `json:"status"` // queued | running | paused | waiting_approval | completed | failed | cancelled
	UserPrompt  string    `json:"user_prompt"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	RiskLevel   int       `json:"risk_level"`
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
	now := time.Now().UTC()
	task := &Task{
		TaskID:      sessionstore.NewID("task"),
		SessionID:   sessionID,
		WorkspaceID: workspaceID,
		Status:      "queued",
		UserPrompt:  prompt,
		CreatedAt:   now,
		UpdatedAt:   now,
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
