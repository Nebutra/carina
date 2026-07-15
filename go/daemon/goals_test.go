package daemon

import (
	"os"
	"path/filepath"
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
