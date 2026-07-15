package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	"github.com/Nebutra/carina/go/statefmt"
)

const goalStoreVersion = 1
const defaultGoalContinuationLimit = 8

type sessionGoal struct {
	SessionID         string    `json:"session_id"`
	Objective         string    `json:"objective"`
	Status            string    `json:"status"`
	TokenBudget       int       `json:"token_budget,omitempty"`
	TokensUsed        int       `json:"tokens_used"`
	TimeUsedSeconds   int64     `json:"time_used_seconds"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	ContinuationsUsed int       `json:"continuations_used"`
	MaxContinuations  int       `json:"max_continuations"`
	UsageBaseline     int       `json:"-"`
	ActiveSince       time.Time `json:"-"`
}

type goalRecord struct {
	Goal          *sessionGoal `json:"goal"`
	UsageBaseline int          `json:"usage_baseline"`
	ActiveSince   time.Time    `json:"active_since,omitempty"`
}
type goalEnvelope struct {
	Version int           `json:"version"`
	Goals   []*goalRecord `json:"goals"`
}
type goalStore struct {
	mu    sync.Mutex
	path  string
	goals map[string]*goalRecord
}

func newGoalStore(stateDir string) *goalStore {
	s := &goalStore{path: filepath.Join(stateDir, "goals.json"), goals: map[string]*goalRecord{}}
	raw, version, ok := statefmt.ReadVersioned(s.path, goalStoreVersion)
	if !ok {
		return s
	}
	var env goalEnvelope
	if json.Unmarshal(raw, &env) != nil {
		_ = statefmt.Quarantine(s.path, version)
		return s
	}
	for _, g := range env.Goals {
		if g != nil && g.Goal != nil {
			g.Goal.UsageBaseline = g.UsageBaseline
			g.Goal.ActiveSince = g.ActiveSince
			s.goals[g.Goal.SessionID] = g
		}
	}
	return s
}
func (s *goalStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	rows := make([]*goalRecord, 0, len(s.goals))
	for _, g := range s.goals {
		g.UsageBaseline = g.Goal.UsageBaseline
		g.ActiveSince = g.Goal.ActiveSince
		rows = append(rows, g)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Goal.SessionID < rows[j].Goal.SessionID })
	raw, err := json.MarshalIndent(goalEnvelope{Version: goalStoreVersion, Goals: rows}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func validGoalStatus(v string) bool {
	switch v {
	case "active", "paused", "blocked", "budget_limited", "usage_limited", "complete":
		return true
	}
	return false
}
func (d *Daemon) sessionTokens(id string) int {
	t := d.usage.costs(id, "", d.providerCatalog).Totals
	return t.InputTokens + t.OutputTokens + t.CacheReadTokens + t.CacheWriteTokens
}
func snapshotGoal(g *sessionGoal, now time.Time) {
	if g.Status == "active" && !g.ActiveSince.IsZero() {
		g.TimeUsedSeconds += int64(now.Sub(g.ActiveSince).Seconds())
		g.ActiveSince = now
	}
}
func publicGoal(g *sessionGoal, now time.Time) sessionGoal {
	c := *g
	snapshotGoal(&c, now)
	c.UsageBaseline = 0
	c.ActiveSince = time.Time{}
	return c
}

func (d *Daemon) handleGoalGet(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	d.goals.mu.Lock()
	defer d.goals.mu.Unlock()
	r := d.goals.goals[p.SessionID]
	if r == nil {
		return map[string]any{"goal": nil}, nil
	}
	g := r.Goal
	g.TokensUsed = max(0, d.sessionTokens(p.SessionID)-g.UsageBaseline)
	now := time.Now().UTC()
	if g.Status == "active" && g.TokenBudget > 0 && g.TokensUsed >= g.TokenBudget {
		before := *g
		snapshotGoal(g, now)
		g.Status = "budget_limited"
		g.UpdatedAt = now
		if err := d.goals.persistLocked(); err != nil {
			*g = before
			return nil, err
		}
	}
	out := publicGoal(g, now)
	return map[string]any{"goal": out}, nil
}
func (d *Daemon) handleGoalSet(params json.RawMessage) (any, error) {
	var p struct {
		SessionID        string `json:"session_id"`
		Objective        string `json:"objective"`
		Status           string `json:"status"`
		TokenBudget      int    `json:"token_budget"`
		MaxContinuations int    `json:"max_continuations"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	p.Objective = strings.TrimSpace(p.Objective)
	if p.Objective == "" {
		return nil, fmt.Errorf("objective is required")
	}
	if p.TokenBudget < 0 {
		return nil, fmt.Errorf("token_budget must be >= 0")
	}
	if p.Status == "" {
		p.Status = "active"
	}
	if !validGoalStatus(p.Status) {
		return nil, fmt.Errorf("invalid goal status %q", p.Status)
	}
	if p.MaxContinuations == 0 {
		p.MaxContinuations = defaultGoalContinuationLimit
	}
	if p.MaxContinuations < 1 || p.MaxContinuations > 32 {
		return nil, fmt.Errorf("max_continuations must be 1..32")
	}
	now := time.Now().UTC()
	g := &sessionGoal{SessionID: p.SessionID, Objective: p.Objective, Status: p.Status, TokenBudget: p.TokenBudget, CreatedAt: now, UpdatedAt: now, MaxContinuations: p.MaxContinuations, UsageBaseline: d.sessionTokens(p.SessionID)}
	if g.Status == "active" {
		g.ActiveSince = now
	}
	d.goals.mu.Lock()
	previous := d.goals.goals[p.SessionID]
	d.goals.goals[p.SessionID] = &goalRecord{Goal: g}
	err := d.goals.persistLocked()
	if err != nil {
		if previous == nil {
			delete(d.goals.goals, p.SessionID)
		} else {
			d.goals.goals[p.SessionID] = previous
		}
	}
	d.goals.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return publicGoal(g, now), nil
}
func (d *Daemon) transitionGoal(params json.RawMessage, status string) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	d.goals.mu.Lock()
	defer d.goals.mu.Unlock()
	r := d.goals.goals[p.SessionID]
	if r == nil {
		return nil, fmt.Errorf("no goal for session %s", p.SessionID)
	}
	now := time.Now().UTC()
	before := *r.Goal
	snapshotGoal(r.Goal, now)
	r.Goal.Status = status
	if status == "active" {
		r.Goal.ActiveSince = now
	}
	r.Goal.UpdatedAt = now
	if err := d.goals.persistLocked(); err != nil {
		*r.Goal = before
		return nil, err
	}
	return publicGoal(r.Goal, now), nil
}
func (d *Daemon) handleGoalPause(p json.RawMessage) (any, error) {
	return d.transitionGoal(p, "paused")
}
func (d *Daemon) handleGoalResume(p json.RawMessage) (any, error) {
	return d.transitionGoal(p, "active")
}
func (d *Daemon) handleGoalComplete(p json.RawMessage) (any, error) {
	return d.transitionGoal(p, "complete")
}
func (d *Daemon) handleGoalClear(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	d.goals.mu.Lock()
	previous, ok := d.goals.goals[p.SessionID]
	delete(d.goals.goals, p.SessionID)
	err := d.goals.persistLocked()
	if err != nil && previous != nil {
		d.goals.goals[p.SessionID] = previous
	}
	d.goals.mu.Unlock()
	return map[string]any{"cleared": ok, "session_id": p.SessionID}, err
}

func (d *Daemon) handleGoalContinue(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	d.goals.mu.Lock()
	r := d.goals.goals[p.SessionID]
	if r == nil {
		d.goals.mu.Unlock()
		return nil, fmt.Errorf("no goal for session %s", p.SessionID)
	}
	g := r.Goal
	g.TokensUsed = max(0, d.sessionTokens(p.SessionID)-g.UsageBaseline)
	if g.Status != "active" {
		d.goals.mu.Unlock()
		return nil, fmt.Errorf("goal is %s, not active", g.Status)
	}
	if g.TokenBudget > 0 && g.TokensUsed >= g.TokenBudget {
		before := *g
		snapshotGoal(g, time.Now().UTC())
		g.Status = "budget_limited"
		if err := d.goals.persistLocked(); err != nil {
			*g = before
			d.goals.mu.Unlock()
			return nil, err
		}
		d.goals.mu.Unlock()
		return nil, fmt.Errorf("goal token budget exhausted")
	}
	if g.ContinuationsUsed >= g.MaxContinuations {
		before := *g
		snapshotGoal(g, time.Now().UTC())
		g.Status = "usage_limited"
		if err := d.goals.persistLocked(); err != nil {
			*g = before
			d.goals.mu.Unlock()
			return nil, err
		}
		d.goals.mu.Unlock()
		return nil, fmt.Errorf("goal continuation limit reached")
	}
	for _, t := range d.sched.List() {
		if t.SessionID == p.SessionID && (t.Status == "queued" || t.Status == "running" || t.Status == "waiting_approval") {
			d.goals.mu.Unlock()
			return nil, fmt.Errorf("session already has an in-flight task %s", t.TaskID)
		}
	}
	objective := g.Objective
	remainingBudget := 0
	if g.TokenBudget > 0 {
		remainingBudget = max(1, g.TokenBudget-g.TokensUsed)
	}
	g.ContinuationsUsed++
	g.UpdatedAt = time.Now().UTC()
	if err := d.goals.persistLocked(); err != nil {
		g.ContinuationsUsed--
		d.goals.mu.Unlock()
		return nil, err
	}
	result, err := d.handleTaskSubmit(mustRaw(map[string]any{"session_id": p.SessionID, "prompt": "Continue working toward this persistent goal. Do not claim completion unless the objective and any explicit success criteria are actually satisfied:\n\n" + objective, "mode": "background"}))
	if err != nil {
		g.ContinuationsUsed--
		if persistErr := d.goals.persistLocked(); persistErr != nil {
			err = fmt.Errorf("%w (also failed to roll back continuation: %v)", err, persistErr)
		}
		d.goals.mu.Unlock()
		return nil, err
	}
	d.goals.mu.Unlock()
	if task, ok := result.(*scheduler.Task); ok && remainingBudget > 0 {
		d.sched.SetTokenBudget(task.TaskID, remainingBudget)
	}
	return result, nil
}
