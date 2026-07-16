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
	SessionID           string                   `json:"session_id"`
	Objective           string                   `json:"objective"`
	Status              string                   `json:"status"`
	TokenBudget         int                      `json:"token_budget,omitempty"`
	TokensUsed          int                      `json:"tokens_used"`
	TimeUsedSeconds     int64                    `json:"time_used_seconds"`
	CreatedAt           time.Time                `json:"created_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
	ContinuationsUsed   int                      `json:"continuations_used"`
	MaxContinuations    int                      `json:"max_continuations"`
	SuccessCriteria     []scheduler.SuccessCheck `json:"success_criteria,omitempty"`
	AutoContinue        bool                     `json:"auto_continue,omitempty"`
	ConsecutiveFailures int                      `json:"consecutive_failures,omitempty"`
	LastTaskID          string                   `json:"last_task_id,omitempty"`
	UsageBaseline       int                      `json:"-"`
	ActiveSince         time.Time                `json:"-"`
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
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	return err
}

func (d *Daemon) auditGoalChange(sessionID, action, from, to string, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["action"], payload["from_status"], payload["to_status"] = action, from, to
	payload["status"] = "prepared"
	return d.recordChecked(sessionID, "GoalChangeRequested", "", "go", payload, "")
}

func (d *Daemon) recordGoalChanged(sessionID, action string, goal *sessionGoal) error {
	return d.recordChecked(sessionID, "GoalChanged", "", "go", map[string]any{"action": action, "status": goal.Status, "tokens_used": goal.TokensUsed, "token_budget": goal.TokenBudget, "continuations_used": goal.ContinuationsUsed, "max_continuations": goal.MaxContinuations}, "")
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
		SessionID        string                   `json:"session_id"`
		Objective        string                   `json:"objective"`
		Status           string                   `json:"status"`
		TokenBudget      int                      `json:"token_budget"`
		MaxContinuations int                      `json:"max_continuations"`
		SuccessCriteria  []scheduler.SuccessCheck `json:"success_criteria"`
		AutoContinue     bool                     `json:"auto_continue"`
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
	g := &sessionGoal{SessionID: p.SessionID, Objective: p.Objective, Status: p.Status, TokenBudget: p.TokenBudget, CreatedAt: now, UpdatedAt: now, MaxContinuations: p.MaxContinuations, UsageBaseline: d.sessionTokens(p.SessionID), SuccessCriteria: append([]scheduler.SuccessCheck(nil), p.SuccessCriteria...), AutoContinue: p.AutoContinue}
	if g.Status == "active" {
		g.ActiveSince = now
	}
	if err := d.auditGoalChange(p.SessionID, "set", "", g.Status, map[string]any{"objective_sha256": hashMemoryQuery(g.Objective), "token_budget": g.TokenBudget, "max_continuations": g.MaxContinuations}); err != nil {
		return nil, fmt.Errorf("goal audit WAL: %w", err)
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
	if err := d.recordGoalChanged(p.SessionID, "set", g); err != nil {
		d.goals.mu.Lock()
		if previous == nil {
			delete(d.goals.goals, p.SessionID)
		} else {
			d.goals.goals[p.SessionID] = previous
		}
		rollbackErr := d.goals.persistLocked()
		d.goals.mu.Unlock()
		if rollbackErr != nil {
			return nil, fmt.Errorf("goal commit audit failed: %v (rollback failed: %v)", err, rollbackErr)
		}
		return nil, fmt.Errorf("goal commit audit: %w", err)
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
	if err := d.auditGoalChange(p.SessionID, status, before.Status, status, nil); err != nil {
		return nil, fmt.Errorf("goal audit WAL: %w", err)
	}
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
	if err := d.recordGoalChanged(p.SessionID, status, r.Goal); err != nil {
		*r.Goal = before
		if rollbackErr := d.goals.persistLocked(); rollbackErr != nil {
			return nil, fmt.Errorf("goal commit audit failed: %v (rollback failed: %v)", err, rollbackErr)
		}
		return nil, fmt.Errorf("goal commit audit: %w", err)
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
	from := ""
	if previous != nil {
		from = previous.Goal.Status
	}
	if err := d.auditGoalChange(p.SessionID, "clear", from, "cleared", nil); err != nil {
		d.goals.mu.Unlock()
		return nil, fmt.Errorf("goal audit WAL: %w", err)
	}
	delete(d.goals.goals, p.SessionID)
	err := d.goals.persistLocked()
	if err != nil && previous != nil {
		d.goals.goals[p.SessionID] = previous
	}
	d.goals.mu.Unlock()
	if err == nil && previous != nil {
		if auditErr := d.recordGoalChanged(p.SessionID, "clear", &sessionGoal{Status: "cleared"}); auditErr != nil {
			d.goals.mu.Lock()
			d.goals.goals[p.SessionID] = previous
			rollbackErr := d.goals.persistLocked()
			d.goals.mu.Unlock()
			if rollbackErr != nil {
				return nil, fmt.Errorf("goal commit audit failed: %v (rollback failed: %v)", auditErr, rollbackErr)
			}
			return nil, fmt.Errorf("goal commit audit: %w", auditErr)
		}
	}
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
	if err := d.auditGoalChange(p.SessionID, "continue", g.Status, g.Status, map[string]any{"continuation": g.ContinuationsUsed, "remaining_token_budget": remainingBudget}); err != nil {
		g.ContinuationsUsed--
		d.goals.mu.Unlock()
		return nil, fmt.Errorf("goal audit WAL: %w", err)
	}
	if err := d.goals.persistLocked(); err != nil {
		g.ContinuationsUsed--
		d.goals.mu.Unlock()
		return nil, err
	}
	if err := d.recordGoalChanged(p.SessionID, "continue", g); err != nil {
		g.ContinuationsUsed--
		rollbackErr := d.goals.persistLocked()
		d.goals.mu.Unlock()
		if rollbackErr != nil {
			return nil, fmt.Errorf("goal commit audit failed: %v (rollback failed: %v)", err, rollbackErr)
		}
		return nil, fmt.Errorf("goal commit audit: %w", err)
	}
	result, err := d.handleTaskSubmit(mustRaw(map[string]any{"session_id": p.SessionID, "prompt": "Continue working toward this persistent goal. Do not claim completion unless the objective and any explicit success criteria are actually satisfied:\n\n" + objective, "mode": "background", "token_budget": remainingBudget, "success_criteria": g.SuccessCriteria}))
	if err != nil {
		g.ContinuationsUsed--
		if persistErr := d.goals.persistLocked(); persistErr != nil {
			err = fmt.Errorf("%w (also failed to roll back continuation: %v)", err, persistErr)
		}
		d.goals.mu.Unlock()
		return nil, err
	}
	d.goals.mu.Unlock()
	if task, ok := result.(*scheduler.Task); ok {
		d.goals.mu.Lock()
		if current := d.goals.goals[p.SessionID]; current != nil {
			current.Goal.LastTaskID = task.TaskID
			_ = d.goals.persistLocked()
		}
		d.goals.mu.Unlock()
		if current, exists := d.sched.Get(task.TaskID); exists && (current.Status == "completed" || current.Status == "failed" || current.Status == "degraded" || current.Status == "cancelled") {
			go d.reconcileGoalTask(current)
		}
	}
	return result, nil
}

func (d *Daemon) reconcileGoalTask(task *scheduler.Task) {
	if task == nil {
		return
	}
	d.goals.mu.Lock()
	r := d.goals.goals[task.SessionID]
	if r == nil || !r.Goal.AutoContinue || r.Goal.Status != "active" || r.Goal.LastTaskID != task.TaskID {
		d.goals.mu.Unlock()
		return
	}
	g := r.Goal
	if task.Status == "completed" {
		before := *g
		snapshotGoal(g, time.Now().UTC())
		g.Status = "complete"
		g.ConsecutiveFailures = 0
		g.UpdatedAt = time.Now().UTC()
		if d.auditGoalChange(task.SessionID, "auto_complete", before.Status, g.Status, map[string]any{"task_id": task.TaskID}) != nil {
			*g = before
			d.goals.mu.Unlock()
			return
		}
		if d.goals.persistLocked() != nil || d.recordGoalChanged(task.SessionID, "auto_complete", g) != nil {
			*g = before
			_ = d.goals.persistLocked()
		}
		d.goals.mu.Unlock()
		return
	}
	g.ConsecutiveFailures++
	if g.ConsecutiveFailures >= 3 {
		before := *g
		g.Status = "blocked"
		g.UpdatedAt = time.Now().UTC()
		if d.auditGoalChange(task.SessionID, "auto_blocked", before.Status, g.Status, map[string]any{"task_id": task.TaskID, "failures": g.ConsecutiveFailures}) == nil {
			_ = d.goals.persistLocked()
			_ = d.recordGoalChanged(task.SessionID, "auto_blocked", g)
		} else {
			*g = before
		}
		d.goals.mu.Unlock()
		return
	}
	_ = d.goals.persistLocked()
	d.goals.mu.Unlock()
	_, _ = d.handleGoalContinue(mustRaw(map[string]any{"session_id": task.SessionID}))
}

func (d *Daemon) recoverAutoGoals() {
	for _, task := range d.sched.List() {
		if task.Status == "completed" || task.Status == "failed" || task.Status == "degraded" || task.Status == "cancelled" {
			go d.reconcileGoalTask(task)
		}
	}
}
