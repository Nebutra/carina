package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoalStorePersistsOneGoalPerSession(t *testing.T) {
	dir := t.TempDir()
	s := newGoalStore(dir)
	now := time.Now().UTC().Truncate(time.Second)
	s.goals["sess-1"] = &goalRecord{Goal: &sessionGoal{
		SessionID: "sess-1", Objective: "ship it", Status: "active",
		TokenBudget: 1000, TokensUsed: 12, CreatedAt: now, UpdatedAt: now,
		MaxContinuations: 8, UsageBaseline: 40, ActiveSince: now,
	}}
	s.mu.Lock()
	if err := s.persistLocked(); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()

	reloaded := newGoalStore(dir)
	g := reloaded.goals["sess-1"]
	if g == nil || g.Goal.Objective != "ship it" || g.Goal.Status != "active" {
		t.Fatalf("reloaded goal = %#v", g)
	}
	if g.UsageBaseline != 40 || g.Goal.MaxContinuations != 8 || g.ActiveSince.IsZero() {
		t.Fatalf("internal governance state lost: %#v", g)
	}
}

func TestGoalStoreDoesNotPersistAutoContinue(t *testing.T) {
	dir := t.TempDir()
	s := newGoalStore(dir)
	now := time.Now().UTC().Truncate(time.Second)
	s.goals["sess-1"] = &goalRecord{Goal: &sessionGoal{
		SessionID: "sess-1", Objective: "ship it", Status: "active",
		AutoContinue: true, TokenBudget: 1000, CreatedAt: now, UpdatedAt: now,
		MaxContinuations: 8, UsageBaseline: 40, ActiveSince: now,
	}}
	s.mu.Lock()
	if err := s.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if !s.goals["sess-1"].Goal.AutoContinue {
		t.Fatal("persist stripped in-process activation")
	}
	s.mu.Unlock()

	raw, err := os.ReadFile(filepath.Join(dir, "goals.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "auto_continue") {
		t.Fatalf("activation persisted to disk: %s", raw)
	}

	reloaded := newGoalStore(dir)
	g := reloaded.goals["sess-1"]
	if g == nil || g.Goal.Objective != "ship it" || g.Goal.Status != "active" {
		t.Fatalf("durable goal lost: %#v", g)
	}
	if g.Goal.AutoContinue {
		t.Fatal("reloaded auto_continue")
	}
}

func TestGoalStoreDisarmsLegacyAutoContinueOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "goals.json")
	legacy := `{"version":1,"goals":[{"goal":{"session_id":"sess-1","objective":"overnight","status":"active","auto_continue":true,"tokens_used":0,"time_used_seconds":0,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","continuations_used":0,"max_continuations":8,"last_task_id":"run-1"},"usage_baseline":0}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newGoalStore(dir)
	g := s.goals["sess-1"]
	if g == nil || g.Goal.Status != "active" || g.Goal.Objective != "overnight" {
		t.Fatalf("legacy goal not loaded: %#v", g)
	}
	if g.Goal.AutoContinue {
		t.Fatal("legacy auto_continue survived load")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"auto_continue":true`) {
		t.Fatalf("legacy activation left on disk: %s", raw)
	}
}

func plantArmedGoalFile(t *testing.T, stateDir, sessionID, lastTaskID string, now time.Time) {
	t.Helper()
	path := filepath.Join(stateDir, "goals.json")
	env := goalEnvelope{Version: goalStoreVersion, Goals: []*goalRecord{{
		Goal: &sessionGoal{
			SessionID: sessionID, Objective: "recover", Status: "active",
			AutoContinue: true, LastTaskID: lastTaskID, CreatedAt: now, UpdatedAt: now,
			MaxContinuations: 8, ActiveSince: now,
		},
		ActiveSince: now,
	}}}
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGoalStoreQuarantinesCorruptState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "goals.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"goals":`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newGoalStore(dir)
	if len(s.goals) != 0 {
		t.Fatalf("corrupt store loaded goals: %#v", s.goals)
	}
	matches, _ := filepath.Glob(path + ".v*.quarantine")
	if len(matches) == 0 {
		t.Fatal("corrupt goal state was not quarantined")
	}
}

func TestGoalStatusesAreClosedSet(t *testing.T) {
	for _, status := range []string{"active", "paused", "blocked", "budget_limited", "usage_limited", "complete"} {
		if !validGoalStatus(status) {
			t.Fatalf("valid status rejected: %s", status)
		}
	}
	if validGoalStatus("completed") || validGoalStatus("") {
		t.Fatal("unknown goal status accepted")
	}
}

func TestSnapshotGoalOnlyChargesActiveTime(t *testing.T) {
	start := time.Unix(100, 0).UTC()
	g := &sessionGoal{Status: "active", ActiveSince: start, TimeUsedSeconds: 3}
	snapshotGoal(g, start.Add(5*time.Second))
	if g.TimeUsedSeconds != 8 {
		t.Fatalf("time used = %d", g.TimeUsedSeconds)
	}
	g.Status = "paused"
	snapshotGoal(g, start.Add(20*time.Second))
	if g.TimeUsedSeconds != 8 {
		t.Fatalf("paused time was charged: %d", g.TimeUsedSeconds)
	}
}
