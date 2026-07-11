// Package workflowui provides the durable operator-facing state machine for
// workflow DAG runs. Execution engines report step transitions; operators can
// pause, resume, stop, restart, inspect, and save a run as a reusable command.
package workflowui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	Queued      Status = "queued"
	Running     Status = "running"
	Paused      Status = "paused"
	Completed   Status = "completed"
	Failed      Status = "failed"
	Stopped     Status = "stopped"
	Interrupted Status = "interrupted"
)

type Step struct {
	ID           string     `json:"id"`
	Status       Status     `json:"status"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Output       string     `json:"output,omitempty"`
	Error        string     `json:"error,omitempty"`
	InputTokens  int64      `json:"input_tokens,omitempty"`
	OutputTokens int64      `json:"output_tokens,omitempty"`
	CostUSD      float64    `json:"cost_usd,omitempty"`
}

type Run struct {
	ID                 string     `json:"id"`
	Workflow           string     `json:"workflow"`
	SessionID          string     `json:"session_id"`
	Input              string     `json:"input,omitempty"`
	Status             Status     `json:"status"`
	Attempt            int        `json:"attempt"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	Steps              []Step     `json:"steps"`
	Resumable          bool       `json:"resumable,omitempty"`
	InterruptedAt      *time.Time `json:"interrupted_at,omitempty"`
	InterruptionReason string     `json:"interruption_reason,omitempty"`
}

type Detail struct {
	Run          Run     `json:"run"`
	Completed    int     `json:"completed"`
	Total        int     `json:"total"`
	Progress     float64 `json:"progress"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type Store struct {
	mu   sync.Mutex
	path string
	runs map[string]Run
	now  func() time.Time
}

func New(stateDir string) (*Store, error) {
	s := &Store{path: filepath.Join(stateDir, "workflow-runs.json"), runs: map[string]Run{}, now: time.Now}
	if raw, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(raw, &s.runs); err != nil {
			return nil, fmt.Errorf("workflowui: load: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) Create(run Run) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.Workflow) == "" {
		return Run{}, errors.New("workflowui: id and workflow are required")
	}
	if _, ok := s.runs[run.ID]; ok {
		return Run{}, fmt.Errorf("workflowui: run %q already exists", run.ID)
	}
	now := s.now().UTC()
	run.Status = Queued
	run.Attempt = 1
	run.CreatedAt = now
	run.UpdatedAt = now
	for i := range run.Steps {
		if run.Steps[i].ID == "" {
			return Run{}, errors.New("workflowui: step id is required")
		}
		run.Steps[i].Status = Queued
	}
	s.runs[run.ID] = run
	if err := s.persistLocked(); err != nil {
		delete(s.runs, run.ID)
		return Run{}, err
	}
	return run, nil
}

func (s *Store) List() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (s *Store) Detail(id string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Detail{}, os.ErrNotExist
	}
	return detail(r), nil
}

func detail(r Run) Detail {
	d := Detail{Run: r, Total: len(r.Steps)}
	for _, st := range r.Steps {
		if st.Status == Completed {
			d.Completed++
		}
		d.InputTokens += st.InputTokens
		d.OutputTokens += st.OutputTokens
		d.CostUSD += st.CostUSD
	}
	if d.Total > 0 {
		d.Progress = float64(d.Completed) / float64(d.Total)
	}
	return d
}

func (s *Store) Transition(id string, target Status) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, os.ErrNotExist
	}
	allowed := map[Status]map[Status]bool{Queued: {Running: true, Paused: true, Stopped: true}, Running: {Paused: true, Completed: true, Failed: true, Stopped: true, Interrupted: true}, Paused: {Running: true, Stopped: true, Interrupted: true}, Interrupted: {Queued: true, Stopped: true}, Failed: {Queued: true}, Stopped: {Queued: true}}
	if !allowed[r.Status][target] {
		return Run{}, fmt.Errorf("workflowui: invalid transition %s -> %s", r.Status, target)
	}
	r.Status = target
	r.UpdatedAt = s.now().UTC()
	old := s.runs[id]
	s.runs[id] = r
	if err := s.persistLocked(); err != nil {
		s.runs[id] = old
		return Run{}, err
	}
	return r, nil
}

// ReconcileStartup marks process-owned runs interrupted while retaining
// completed outputs. An uncommitted running step is safe to retry.
func (s *Store) ReconcileStartup(reason string) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	var changed []Run
	old := make(map[string]Run)
	for id, r := range s.runs {
		if r.Status != Running && r.Status != Paused {
			continue
		}
		r.Status = Interrupted
		r.Resumable = true
		r.InterruptedAt = &now
		r.InterruptionReason = reason
		r.UpdatedAt = now
		for i := range r.Steps {
			if r.Steps[i].Status == Running {
				r.Steps[i].Status = Queued
				r.Steps[i].StartedAt = nil
			}
		}
		old[id] = s.runs[id]
		s.runs[id] = r
		changed = append(changed, r)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := s.persistLocked(); err != nil {
		for id, r := range old {
			s.runs[id] = r
		}
		return nil, err
	}
	return changed, nil
}

func (s *Store) Restart(id, newID string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.runs[id]
	if !ok {
		return Run{}, os.ErrNotExist
	}
	if old.Status != Failed && old.Status != Stopped && old.Status != Completed {
		return Run{}, errors.New("workflowui: only terminal runs can restart")
	}
	if newID == "" {
		return Run{}, errors.New("workflowui: new id is required")
	}
	if _, ok := s.runs[newID]; ok {
		return Run{}, errors.New("workflowui: new id already exists")
	}
	now := s.now().UTC()
	old.ID = newID
	old.Status = Queued
	old.Attempt++
	old.CreatedAt = now
	old.UpdatedAt = now
	old.Resumable = false
	old.InterruptedAt = nil
	old.InterruptionReason = ""
	for i := range old.Steps {
		old.Steps[i] = Step{ID: old.Steps[i].ID, Status: Queued}
	}
	s.runs[newID] = old
	if err := s.persistLocked(); err != nil {
		delete(s.runs, newID)
		return Run{}, err
	}
	return old, nil
}

func (s *Store) UpdateStep(runID string, step Step) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok {
		return Run{}, os.ErrNotExist
	}
	found := false
	for i := range r.Steps {
		if r.Steps[i].ID == step.ID {
			r.Steps[i] = step
			found = true
			break
		}
	}
	if !found {
		return Run{}, fmt.Errorf("workflowui: unknown step %q", step.ID)
	}
	r.UpdatedAt = s.now().UTC()
	original := s.runs[runID]
	s.runs[runID] = r
	if err := s.persistLocked(); err != nil {
		s.runs[runID] = original
		return Run{}, err
	}
	return r, nil
}

// MarkInterrupted records an explicit recovery state after an external side
// effect succeeded but its normal result transition could not be persisted.
func (s *Store) MarkInterrupted(id, reason string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, os.ErrNotExist
	}
	old := r
	now := s.now().UTC()
	r.Status = Interrupted
	r.Resumable = false
	r.InterruptedAt = &now
	r.InterruptionReason = reason
	r.UpdatedAt = now
	s.runs[id] = r
	if err := s.persistLocked(); err != nil {
		s.runs[id] = old
		return Run{}, err
	}
	return r, nil
}

func (s *Store) SaveCommand(id, dir, name string) (string, error) {
	d, err := s.Detail(id)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(name, "/\\") || strings.TrimSpace(name) == "" {
		return "", errors.New("workflowui: invalid command name")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	body := fmt.Sprintf("---\nname: %s\ndescription: Run saved workflow %s\n---\nRun workflow %q with input: $ARGUMENTS\n", name, d.Run.Workflow, d.Run.Workflow)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.runs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
