package scheduler

import (
	"testing"
	"time"
)

func TestScheduleStorePersistsAndClaimsEvery(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store := OpenScheduleStore(dir)
	row, err := store.Create("sess_1", "run checks", "every", "5m", now)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.ClaimDue(now.Add(4 * time.Minute)); len(got) != 0 {
		t.Fatalf("schedule claimed too early: %+v", got)
	}
	reloaded := OpenScheduleStore(dir)
	due := reloaded.ClaimDue(now.Add(5 * time.Minute))
	if len(due) != 1 || due[0].ScheduleID != row.ScheduleID {
		t.Fatalf("persisted schedule not claimed: %+v", due)
	}
	rows := reloaded.List()
	if len(rows) != 1 || !rows[0].NextRunAt.After(now.Add(5*time.Minute)) {
		t.Fatalf("recurring schedule did not advance: %+v", rows)
	}
}

func TestScheduleStoreAtRunsOnce(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	at := now.Add(time.Minute)
	store := OpenScheduleStore(dir)
	if _, err := store.Create("sess_1", "one shot", "at", at.Format(time.RFC3339), now); err != nil {
		t.Fatal(err)
	}
	if due := store.ClaimDue(at); len(due) != 1 {
		t.Fatalf("expected one due schedule, got %+v", due)
	}
	if due := store.ClaimDue(at.Add(time.Hour)); len(due) != 0 {
		t.Fatalf("one-shot schedule fired twice: %+v", due)
	}
}

func TestScheduleStoreListIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store := OpenScheduleStore(dir)
	late, err := store.Create("sess_1", "late", "every", "10m", now)
	if err != nil {
		t.Fatal(err)
	}
	early, err := store.Create("sess_1", "early", "every", "1m", now)
	if err != nil {
		t.Fatal(err)
	}
	rows := store.List()
	if len(rows) != 2 || rows[0].ScheduleID != early.ScheduleID || rows[1].ScheduleID != late.ScheduleID {
		t.Fatalf("schedules not sorted by next_run_at: %+v", rows)
	}
}

func TestCronScheduleSupportsSteps(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 2, 30, 0, time.UTC)
	next, err := nextCronTime("*/5 * * * *", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 10, 8, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next cron = %s, want %s", next, want)
	}
}
