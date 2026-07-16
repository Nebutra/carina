package scheduler

import (
	"os"
	"path/filepath"
	"strings"
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

func TestScheduleEnvelopePersistsAndDefaultsToForbidOverlap(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	store := OpenScheduleStore(dir)
	row, err := store.CreateWithEnvelope(Schedule{SessionID: "sess_1", Prompt: "run", Kind: "every", Expression: "1m", Model: "openai/gpt-5", ReasoningEffort: "high", Agent: "build", Mode: "background", PermissionProfile: "safe-edit", ApprovalMode: "on_request"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if row.ConcurrencyPolicy != "forbid" {
		t.Fatalf("policy=%q", row.ConcurrencyPolicy)
	}
	reloaded := OpenScheduleStore(dir).List()[0]
	if reloaded.Model != "openai/gpt-5" || reloaded.ReasoningEffort != "high" || reloaded.PermissionProfile != "safe-edit" || reloaded.ConcurrencyPolicy != "forbid" {
		t.Fatalf("envelope not persisted: %+v", reloaded)
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

func TestScheduleStoreRecoversValidTempFileWhenMainIsMissing(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	raw := `[{"schedule_id":"sched_1","session_id":"sess_1","prompt":"recover","kind":"every","expression":"5m","enabled":true,"next_run_at":"` + now.Add(time.Minute).Format(time.RFC3339) + `","created_at":"` + now.Format(time.RFC3339) + `","updated_at":"` + now.Format(time.RFC3339) + `"}]`
	if err := os.WriteFile(filepath.Join(dir, "schedules.json.tmp"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	store := OpenScheduleStore(dir)
	rows := store.List()
	if len(rows) != 1 || rows[0].ScheduleID != "sched_1" {
		t.Fatalf("temp schedule was not recovered: %+v", rows)
	}
	if _, err := os.Stat(filepath.Join(dir, "schedules.json")); err != nil {
		t.Fatalf("recovered schedule file missing: %v", err)
	}
}

func TestScheduleStoreQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schedules.json"), []byte(`{not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := OpenScheduleStore(dir)
	if rows := store.List(); len(rows) != 0 {
		t.Fatalf("corrupt schedule file should not load rows: %+v", rows)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "schedules.json.corrupt.") {
			found = true
		}
	}
	if !found {
		t.Fatalf("corrupt schedule file was not quarantined: %+v", entries)
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
