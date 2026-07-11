// Package agentview builds the stable, client-agnostic roster used by Agent View.
package agentview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

type Store struct {
	mu   sync.Mutex
	path string
	rows map[string]Metadata
}

func Open(stateDir string) *Store {
	s := &Store{path: filepath.Join(stateDir, "agent-view.json"), rows: map[string]Metadata{}}
	if raw, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(raw, &s.rows)
	}
	return s
}
func (s *Store) Snapshot() map[string]Metadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Metadata, len(s.rows))
	for k, v := range s.rows {
		out[k] = v
	}
	return out
}
func (s *Store) Set(sessionID string, value Metadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[sessionID] = value
	return s.persist()
}
func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, sessionID)
	return s.persist()
}
func (s *Store) persist() error {
	raw, err := json.MarshalIndent(s.rows, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("agentview: persist: %w", err)
	}
	return os.Rename(tmp, s.path)
}

type Category string

const (
	NeedsInput Category = "needs_input"
	Working    Category = "working"
	Completed  Category = "completed"
)

type Metadata struct {
	Title       string `json:"title,omitempty"`
	PullRequest string `json:"pull_request,omitempty"`
	Branch      string `json:"branch,omitempty"`
	WorktreeID  string `json:"worktree_id,omitempty"`
}

type Entry struct {
	SessionID string    `json:"session_id"`
	TaskID    string    `json:"task_id,omitempty"`
	ParentID  string    `json:"parent_id,omitempty"`
	Workspace string    `json:"workspace"`
	Status    string    `json:"status"`
	Category  Category  `json:"category"`
	Prompt    string    `json:"prompt,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Model     string    `json:"model,omitempty"`
	Needs     string    `json:"needs,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Metadata  Metadata  `json:"metadata,omitempty"`
}

type Roster struct {
	NeedsInput []Entry `json:"needs_input"`
	Working    []Entry `json:"working"`
	Completed  []Entry `json:"completed"`
}

func Build(sessions []*sessionstore.Session, tasks []*scheduler.Task, pendingQuestions map[string]string, metadata map[string]Metadata) Roster {
	bySession := make(map[string][]*scheduler.Task)
	for _, task := range tasks {
		if task != nil {
			bySession[task.SessionID] = append(bySession[task.SessionID], task)
		}
	}
	var roster Roster
	for _, session := range sessions {
		if session == nil {
			continue
		}
		list := bySession[session.SessionID]
		if len(list) == 0 {
			e := Entry{SessionID: session.SessionID, ParentID: session.ParentID, Workspace: session.WorkspaceRoot, Status: session.Status, Category: classify(session.Status), UpdatedAt: session.CreatedAt, Metadata: metadata[session.SessionID]}
			appendEntry(&roster, e)
			continue
		}
		for _, task := range list {
			e := Entry{SessionID: session.SessionID, TaskID: task.TaskID, ParentID: session.ParentID, Workspace: session.WorkspaceRoot, Status: task.Status, Category: classify(task.Status), Prompt: task.UserPrompt, Summary: task.Summary, Agent: task.Agent, Model: task.Model, UpdatedAt: task.UpdatedAt, Metadata: metadata[session.SessionID]}
			if need := pendingQuestions[task.TaskID]; need != "" {
				e.Category, e.Needs = NeedsInput, need
			}
			appendEntry(&roster, e)
		}
	}
	sortRoster(&roster)
	return roster
}

func classify(status string) Category {
	switch status {
	case "waiting_input", "waiting_approval", "paused":
		return NeedsInput
	case "completed", "failed", "cancelled", "closed", "degraded":
		return Completed
	default:
		return Working
	}
}

func appendEntry(r *Roster, e Entry) {
	switch e.Category {
	case NeedsInput:
		r.NeedsInput = append(r.NeedsInput, e)
	case Completed:
		r.Completed = append(r.Completed, e)
	default:
		r.Working = append(r.Working, e)
	}
}

func sortRoster(r *Roster) {
	less := func(items []Entry) {
		sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	}
	less(r.NeedsInput)
	less(r.Working)
	less(r.Completed)
}
