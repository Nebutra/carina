package daemon

import (
	"context"
	"errors"
	"fmt"
	"github.com/Nebutra/carina/go/scheduler"
	"github.com/Nebutra/carina/go/statefmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPatchSuffixRejectsDivergedLineage(t *testing.T) {
	got, err := patchSuffix([]string{"p1", "p2", "p3"}, []string{"p1", "p2"})
	if err != nil || len(got) != 1 || got[0] != "p3" {
		t.Fatalf("suffix=%v err=%v", got, err)
	}
	if _, err := patchSuffix([]string{"p1", "other"}, []string{"p1", "p2"}); err == nil {
		t.Fatal("expected divergent lineage refusal")
	}
}

func TestCheckpointListIsGloballyOldestToNewestAcrossTasks(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	older := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "older")
	newer := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "newer")
	olderFirst := &runCheckpoint{Turn: 1, Transcript: newTranscript(older.UserPrompt)}
	newerFirst := &runCheckpoint{Turn: 1, Transcript: newTranscript(newer.UserPrompt)}
	newerSecond := &runCheckpoint{Turn: 2, Transcript: newTranscript(newer.UserPrompt)}
	olderLate := &runCheckpoint{Turn: 2, Transcript: newTranscript(older.UserPrompt)}
	d.runs.saveCheckpoint(older.TaskID, olderFirst)
	d.runs.saveCheckpoint(newer.TaskID, newerFirst)
	d.runs.saveCheckpoint(newer.TaskID, newerSecond)
	d.runs.saveCheckpoint(older.TaskID, olderLate)

	resultAny, err := d.handleCheckpointList(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	rows := resultAny.([]any)
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.(map[string]any)["checkpoint_id"].(string))
	}
	want := []string{checkpointID(older, olderFirst), checkpointID(newer, newerFirst), checkpointID(newer, newerSecond), checkpointID(older, olderLate)}
	if !slices.Equal(got, want) {
		t.Fatalf("checkpoint order = %v, want %v", got, want)
	}
	if got[len(got)-1] != checkpointID(older, olderLate) {
		t.Fatalf("last checkpoint must be newest, got %s", got[len(got)-1])
	}
}

func TestCheckpointListParsesLegacySecondAndNewNanosecondTimes(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	legacyTask := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "legacy")
	newTask := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "new")
	base := time.Date(2026, 7, 14, 12, 0, 5, 0, time.UTC)
	legacyDir := filepath.Join(d.runs.dir, legacyTask.TaskID+".ckpts")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "00000000000000000001.json")
	if err := os.WriteFile(legacyPath, []byte(`{"version":1,"turn":1,"transcript":{"task":"legacy"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(legacyPath, base, base); err != nil {
		t.Fatal(err)
	}
	newCP := &runCheckpoint{CheckpointID: newTask.TaskID + ":1:7", CreatedAt: base.Add(time.Nanosecond).Format(time.RFC3339Nano), Sequence: 7, Turn: 1, Transcript: newTranscript("new")}
	if err := d.runs.saveCheckpointChecked(newTask.TaskID, newCP); err != nil {
		t.Fatal(err)
	}
	resultAny, err := d.handleCheckpointList(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	rows := resultAny.([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if got := rows[0].(map[string]any)["checkpoint_id"]; got != legacyTask.TaskID+":1" {
		t.Fatalf("legacy exact-second checkpoint sorted after nanosecond checkpoint: %+v", rows)
	}
}

func TestLoadCheckpointTurnParsesLegacySecondAndNewNanosecondTimes(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	base := time.Date(2026, 7, 14, 12, 0, 5, 0, time.UTC)
	dir := filepath.Join(runs.dir, "task.ckpts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(dir, "00000000000000000001.json")
	if err := os.WriteFile(legacyPath, []byte(`{"version":1,"turn":1,"transcript":{"task":"legacy"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(legacyPath, base, base); err != nil {
		t.Fatal(err)
	}
	newCP := &runCheckpoint{CheckpointID: "task:1:7", CreatedAt: base.Add(time.Nanosecond).Format(time.RFC3339Nano), Sequence: 7, Turn: 1, Transcript: newTranscript("new")}
	if err := runs.saveCheckpointChecked("task", newCP); err != nil {
		t.Fatal(err)
	}
	selected := runs.loadCheckpointTurn("task", 1)
	if selected == nil || selected.CheckpointID != newCP.CheckpointID {
		t.Fatalf("loadCheckpointTurn selected %+v, want nanosecond-newer %+v", selected, newCP)
	}
}

func TestRunStoreCheckpointPublishIsHistoryFirst(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	if err := runs.saveCheckpointChecked("task", &runCheckpoint{Turn: 1, Transcript: newTranscript("x")}); err != nil {
		t.Fatal(err)
	}
	// Replacing the history directory with a file injects a history write failure.
	if err := os.RemoveAll(filepath.Join(runs.dir, "task.ckpts")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs.dir, "task.ckpts"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runs.saveCheckpointChecked("task", &runCheckpoint{Turn: 2, Transcript: newTranscript("x")}); err == nil {
		t.Fatal("expected history failure")
	}
	if latest := runs.loadCheckpoint("task"); latest == nil || latest.Turn != 1 {
		t.Fatalf("latest advanced without history: %+v", latest)
	}
}

func TestCheckpointHistoryBranchesWithoutOverwritingOldTurn(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	cp1 := &runCheckpoint{Turn: 1, Transcript: newTranscript("x")}
	oldTurn2 := &runCheckpoint{Turn: 2, Transcript: newTranscript("x")}
	if err := runs.saveCheckpointChecked("task", cp1); err != nil {
		t.Fatal(err)
	}
	if err := runs.saveCheckpointChecked("task", oldTurn2); err != nil {
		t.Fatal(err)
	}
	if err := runs.publishCheckpointLatest("task", cp1); err != nil {
		t.Fatal(err)
	}
	branchTurn2 := &runCheckpoint{Turn: 2, Transcript: newTranscript("x")}
	if err := runs.saveCheckpointChecked("task", branchTurn2); err != nil {
		t.Fatal(err)
	}
	if oldTurn2.CheckpointID == branchTurn2.CheckpointID {
		t.Fatalf("branched turn reused checkpoint id %s", oldTurn2.CheckpointID)
	}
	if branchTurn2.ParentCheckpointID != cp1.CheckpointID {
		t.Fatalf("branch parent = %s, want %s", branchTurn2.ParentCheckpointID, cp1.CheckpointID)
	}
	if got := runs.loadCheckpointID("task", oldTurn2.CheckpointID); got == nil || got.CheckpointID != oldTurn2.CheckpointID {
		t.Fatalf("old branch checkpoint was overwritten: %+v", got)
	}
	if history := runs.listCheckpoints("task"); len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
}

func TestLegacyTurnCheckpointGetsStableCompatibilityIdentityAndTime(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	dir := filepath.Join(runs.dir, "task.ckpts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "00000000000000000001.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"turn":1,"transcript":{"task":"legacy"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, legacyTime, legacyTime); err != nil {
		t.Fatal(err)
	}
	history := runs.listCheckpoints("task")
	if len(history) != 1 || runCheckpointID("task", history[0]) != "task:1" {
		t.Fatalf("legacy history = %+v", history)
	}
	if history[0].CreatedAt != legacyTime.Format(time.RFC3339Nano) || history[0].Sequence != 0 {
		t.Fatalf("legacy compatibility metadata = %+v", history[0])
	}
	if loaded := runs.loadCheckpointID("task", "task:1"); loaded == nil || loaded.Turn != 1 {
		t.Fatalf("legacy checkpoint lookup = %+v", loaded)
	}
	newCheckpoint := &runCheckpoint{Turn: 2, Transcript: newTranscript("new")}
	if err := runs.saveCheckpointChecked("task", newCheckpoint); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(runs.dir, "task.ckpts", fmt.Sprintf("%020d.json", newCheckpoint.Sequence)))
	if err != nil || !strings.Contains(string(raw), `"version":2`) {
		t.Fatalf("new checkpoint must use v2, raw=%s err=%v", raw, err)
	}
	oldReaderPath := filepath.Join(runs.dir, "old-reader.ckpt.json")
	if err := os.WriteFile(oldReaderPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, version, ok := statefmt.ReadVersioned(oldReaderPath, 1); ok || version != 2 {
		t.Fatalf("v1 reader accepted v2 checkpoint: version=%d ok=%v", version, ok)
	}
}

func TestCheckpointOrphanHistoryCanRepublishLatestButCannotMutate(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	cp := &runCheckpoint{Turn: 1, Transcript: newTranscript("x")}
	latestPath := filepath.Join(runs.dir, "task.ckpt.json")
	if err := os.Mkdir(latestPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runs.saveCheckpointChecked("task", cp); err == nil {
		t.Fatal("expected latest publish failure")
	}
	if err := os.RemoveAll(latestPath); err != nil {
		t.Fatal(err)
	}
	if err := runs.saveCheckpointChecked("task", cp); err != nil {
		t.Fatalf("identical orphan retry should publish latest: %v", err)
	}
	if latest := runs.loadCheckpoint("task"); latest == nil || latest.CheckpointID != cp.CheckpointID {
		t.Fatalf("latest after orphan retry = %+v", latest)
	}
	mutated := *cp
	mutated.Transcript = newTranscript("changed")
	if err := runs.saveCheckpointChecked("task", &mutated); err == nil || !strings.Contains(err.Error(), "conflicts with immutable") {
		t.Fatalf("mutated orphan retry error = %v", err)
	}
}

func TestRunStoreTombstonePreventsRestartResurrection(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	task := &scheduler.Task{TaskID: "task", Status: "completed"}
	runs.save(task)
	if err := runs.tombstone(task.TaskID); err != nil {
		t.Fatal(err)
	}
	if got := runs.load(); len(got) != 0 {
		t.Fatalf("tombstoned run resurrected: %+v", got)
	}
}

func TestRestoreJournalBecomesBlockedOnRestart(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	if err := runs.writeRestoreJournal("task", &restoreJournal{
		Version: restoreJournalVersion, CheckpointID: "task:1", TargetTurn: 1,
		Pending: []string{"p1"}, State: "rolling_back", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	ids, err := runs.reconcileRestoreJournals()
	if err != nil || len(ids) != 1 || ids[0] != "task" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	raw, err := os.ReadFile(filepath.Join(runs.dir, "task.restore.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "blocked_reconciliation_required") {
		t.Fatalf("journal not blocked: %s", raw)
	}
}

func TestCheckpointRestorePublishesLatestPersistsAndIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	d1 := newDaemonAt(t, stateDir)
	sess, err := d1.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d1.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d1.sched.Submit(sess.SessionID, sess.WorkspaceID, "restore me")
	d1.sched.SetStatus(task.TaskID, "completed")
	if current, ok := d1.sched.Get(task.TaskID); !ok || d1.runs.saveChecked(current) != nil {
		t.Fatalf("persist seed task: ok=%v task=%+v", ok, current)
	}
	cp1 := &runCheckpoint{Turn: 1, Transcript: newTranscript(task.UserPrompt)}
	cp2 := &runCheckpoint{Turn: 2, Transcript: newTranscript(task.UserPrompt)}
	if err := d1.runs.saveCheckpointChecked(task.TaskID, cp1); err != nil {
		t.Fatal(err)
	}
	if err := d1.runs.saveCheckpointChecked(task.TaskID, cp2); err != nil {
		t.Fatal(err)
	}

	params := mustJSON(t, map[string]any{"session_id": sess.SessionID, "checkpoint_id": checkpointID(task, cp1), "confirmed": true})
	resultAny, err := d1.handleCheckpointRestore(params)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	result := resultAny.(map[string]any)
	if result["status"] != "paused" || result["idempotent"] != false {
		t.Fatalf("restore result = %+v", result)
	}
	if latest := d1.runs.loadCheckpoint(task.TaskID); latest == nil || latest.Turn != 1 {
		t.Fatalf("latest checkpoint = %+v, want turn 1", latest)
	}
	persisted := taskByID(d1.runs.load(), task.TaskID)
	if persisted == nil || persisted.Status != "paused" || persisted.ReconciliationRequired {
		t.Fatalf("persisted restored task = %+v", persisted)
	}
	resultAny, err = d1.handleCheckpointRestore(params)
	if err != nil || resultAny.(map[string]any)["idempotent"] != true {
		t.Fatalf("idempotent restore = %+v, err=%v", resultAny, err)
	}
	d1.Close()

	d2 := newDaemonAt(t, stateDir)
	defer d2.Close()
	recovered, ok := d2.sched.Get(task.TaskID)
	if !ok || recovered.Status != "paused" || recovered.ReconciliationRequired {
		t.Fatalf("recovered restored task = %+v, ok=%v", recovered, ok)
	}
	if latest := d2.runs.loadCheckpoint(task.TaskID); latest == nil || latest.Turn != 1 {
		t.Fatalf("recovered latest checkpoint = %+v", latest)
	}
}

func TestCheckpointRestoreFailureSurvivesRestartAndSameTargetReconciles(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	d1 := newDaemonAt(t, stateDir)
	sess, err := d1.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d1.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d1.sched.Submit(sess.SessionID, sess.WorkspaceID, "restore me")
	d1.sched.SetStatus(task.TaskID, "completed")
	current, _ := d1.sched.Get(task.TaskID)
	if err := d1.runs.saveChecked(current); err != nil {
		t.Fatal(err)
	}
	cp1 := &runCheckpoint{Turn: 1, Transcript: newTranscript(task.UserPrompt)}
	cp2 := &runCheckpoint{Turn: 2, Transcript: newTranscript(task.UserPrompt)}
	d1.runs.saveCheckpoint(task.TaskID, cp1)
	d1.runs.saveCheckpoint(task.TaskID, cp2)

	latestPath := filepath.Join(d1.runs.dir, task.TaskID+".ckpt.json")
	if err := os.Remove(latestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(latestPath, 0o700); err != nil {
		t.Fatal(err)
	}
	params := mustJSON(t, map[string]any{"session_id": sess.SessionID, "checkpoint_id": checkpointID(task, cp1), "confirmed": true})
	if _, err := d1.handleCheckpointRestore(params); err == nil || !strings.Contains(err.Error(), "checkpoint_restore_blocked") {
		t.Fatalf("restore error = %v", err)
	}
	journal, err := d1.runs.loadRestoreJournal(task.TaskID)
	if err != nil || journal == nil || journal.State != "blocked_reconciliation_required" {
		t.Fatalf("blocked journal = %+v, err=%v", journal, err)
	}
	blocked, _ := d1.sched.Get(task.TaskID)
	if !blocked.ReconciliationRequired || blocked.Status != "paused" {
		t.Fatalf("blocked task = %+v", blocked)
	}
	d1.Close()

	d2 := newDaemonAt(t, stateDir)
	defer d2.Close()
	recovered, ok := d2.sched.Get(task.TaskID)
	if !ok || !recovered.ReconciliationRequired || recovered.Status != "paused" {
		t.Fatalf("recovered blocked task = %+v, ok=%v", recovered, ok)
	}
	if err := os.RemoveAll(latestPath); err != nil {
		t.Fatal(err)
	}
	resultAny, err := d2.handleCheckpointRestore(params)
	if err != nil {
		t.Fatalf("same-target reconciliation: %v", err)
	}
	result := resultAny.(map[string]any)
	if result["status"] != "paused" || result["reconciliation_required"] != false {
		t.Fatalf("reconciliation result = %+v", result)
	}
	if journal, err := d2.runs.loadRestoreJournal(task.TaskID); err != nil || journal != nil {
		t.Fatalf("journal after reconciliation = %+v, err=%v", journal, err)
	}
	reconciled, _ := d2.sched.Get(task.TaskID)
	if reconciled.ReconciliationRequired || reconciled.Status != "paused" {
		t.Fatalf("reconciled task = %+v", reconciled)
	}
}

func TestCheckpointRestoreCompletionAuditIsExactlyOnceAcrossCrashWindows(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		failState               string
		completionBeforeRestart int
	}{
		{name: "after task persist before completion audit", failState: "audit_completion", completionBeforeRestart: 0},
		{name: "after completion audit before commit marker", failState: "committed", completionBeforeRestart: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			workspace := t.TempDir()
			d1 := newDaemonAt(t, stateDir)
			sess, err := d1.store.CreateSession(workspace, "safe-edit")
			if err != nil {
				t.Fatal(err)
			}
			if err := d1.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
				t.Fatal(err)
			}
			task := d1.sched.Submit(sess.SessionID, sess.WorkspaceID, "restore audit")
			d1.sched.SetStatus(task.TaskID, "completed")
			current, _ := d1.sched.Get(task.TaskID)
			if err := d1.runs.saveChecked(current); err != nil {
				t.Fatal(err)
			}
			cp := &runCheckpoint{Turn: 1, Transcript: newTranscript(task.UserPrompt)}
			if err := d1.runs.saveCheckpointChecked(task.TaskID, cp); err != nil {
				t.Fatal(err)
			}
			failed := false
			d1.runs.restoreJournalWriteHook = func(_ string, journal *restoreJournal) error {
				if !failed && journal.State == tc.failState {
					failed = true
					return errors.New("injected restore crash window")
				}
				return nil
			}
			params := mustJSON(t, map[string]any{"session_id": sess.SessionID, "checkpoint_id": checkpointID(task, cp), "confirmed": true})
			if _, err := d1.handleCheckpointRestore(params); err == nil || !strings.Contains(err.Error(), "checkpoint_restore_blocked") {
				t.Fatalf("restore crash-window error = %v", err)
			}
			journal, err := d1.runs.loadRestoreJournal(task.TaskID)
			if err != nil || journal == nil || journal.OperationID == "" {
				t.Fatalf("restore journal = %+v, err=%v", journal, err)
			}
			operationID := journal.OperationID
			count, err := d1.restoreAuditPhaseCount(sess.SessionID, task.TaskID, operationID, "completion")
			if err != nil || count != tc.completionBeforeRestart {
				t.Fatalf("completion count before restart = %d, err=%v", count, err)
			}
			d1.Close()

			d2 := newDaemonAt(t, stateDir)
			defer d2.Close()
			if _, err := d2.handleCheckpointRestore(params); err != nil {
				t.Fatalf("restore retry: %v", err)
			}
			count, err = d2.restoreAuditPhaseCount(sess.SessionID, task.TaskID, operationID, "completion")
			if err != nil || count != 1 {
				t.Fatalf("completion count after retry = %d, err=%v", count, err)
			}
			if journal, err := d2.runs.loadRestoreJournal(task.TaskID); err != nil || journal != nil {
				t.Fatalf("journal after committed retry = %+v, err=%v", journal, err)
			}
		})
	}
}

func TestCheckpointRestoreRequiresSessionQuiescenceAndExecutionFence(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	target := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "target")
	d.sched.SetStatus(target.TaskID, "completed")
	cp := &runCheckpoint{Turn: 1, Transcript: newTranscript(target.UserPrompt)}
	d.runs.saveCheckpoint(target.TaskID, cp)
	params := mustJSON(t, map[string]any{"session_id": sess.SessionID, "checkpoint_id": checkpointID(target, cp), "confirmed": true})

	other := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "other")
	d.sched.SetStatus(other.TaskID, "running")
	if _, err := d.handleCheckpointRestore(params); err == nil || !strings.Contains(err.Error(), "must be quiescent") {
		t.Fatalf("restore with running sibling error = %v", err)
	}
	d.sched.SetStatus(other.TaskID, "completed")

	fence := d.sessionExecutionFence(sess.SessionID)
	fence.RLock()
	if _, err := d.handleCheckpointRestore(params); err == nil || !strings.Contains(err.Error(), "mutation in flight") {
		fence.RUnlock()
		t.Fatalf("restore with in-flight mutation error = %v", err)
	}
	fence.RUnlock()
	if latest := d.runs.loadCheckpoint(target.TaskID); latest == nil || latest.CheckpointID != cp.CheckpointID {
		t.Fatalf("refused restore changed latest: %+v", latest)
	}
}

type blockingResumeReasoner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingResumeReasoner) Name() string { return "blocking-resume" }
func (r *blockingResumeReasoner) Think(ctx context.Context, _ string) (string, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return `{"tool":"done","summary":"resumed"}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestTaskResumePersistsRunningBeforeStartingFromLatest(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	d := newDaemonAt(t, stateDir)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "resume me")
	d.sched.SetStatus(task.TaskID, "paused")
	paused, _ := d.sched.Get(task.TaskID)
	if err := d.runs.saveChecked(paused); err != nil {
		t.Fatal(err)
	}
	tr := newTranscript(task.UserPrompt)
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read", Obs: Observation{Content: "done"}})
	if err := d.runs.saveCheckpointChecked(task.TaskID, &runCheckpoint{Turn: 1, Transcript: tr}); err != nil {
		t.Fatal(err)
	}
	reasoner := &blockingResumeReasoner{started: make(chan struct{}), release: make(chan struct{})}
	d.SetReasoner(reasoner)
	resultAny, err := d.handleTaskResume(mustJSON(t, map[string]any{"task_id": task.TaskID}))
	if err != nil {
		t.Fatal(err)
	}
	if result := resultAny.(*scheduler.Task); result.TaskID != task.TaskID || result.Status != "running" {
		t.Fatalf("resume result = %+v", result)
	}
	select {
	case <-reasoner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed task did not start")
	}
	if persisted := taskByID(newRunStore(stateDir).load(), task.TaskID); persisted == nil || persisted.Status != "running" {
		t.Fatalf("task was not persisted running before reasoner start: %+v", persisted)
	}
	if _, err := d.handleTaskResume(mustJSON(t, map[string]any{"task_id": task.TaskID})); err == nil {
		t.Fatal("running task must reject a second resume")
	}
	close(reasoner.release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if current, _ := d.sched.Get(task.TaskID); current.Status == "completed" {
			if current.Summary != "resumed" {
				t.Fatalf("resume summary = %q", current.Summary)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	current, _ := d.sched.Get(task.TaskID)
	t.Fatalf("resumed task did not complete: %+v", current)
}

func TestCheckpointPersistenceFailureStopsRunBeforeNextAction(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "checkpoint")
	d.sched.SetStatus(task.TaskID, "running")
	if err := os.WriteFile(filepath.Join(d.runs.dir, task.TaskID+".ckpts"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d.persistTurnCheckpoint(sess, task, newTranscript(task.UserPrompt), 1, "") {
		t.Fatal("checkpoint failure must stop the run")
	}
	current, _ := d.sched.Get(task.TaskID)
	if current.Status != "degraded" || !strings.Contains(current.Summary, "prevent stale replay") {
		t.Fatalf("task after checkpoint failure = %+v", current)
	}
}

func taskByID(tasks []*scheduler.Task, taskID string) *scheduler.Task {
	for _, task := range tasks {
		if task.TaskID == taskID {
			return task
		}
	}
	return nil
}

func TestAgentDispatchRollsBackSessionOnSubmitFailure(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	before := len(d.store.List())
	_, err := d.handleAgentDispatch(agentViewRaw(map[string]any{"workspace_root": t.TempDir(), "prompt": "x", "agent": "does-not-exist"}))
	if err == nil {
		t.Fatal("expected submit failure")
	}
	if got := len(d.store.List()); got != before {
		t.Fatalf("session leaked: before=%d after=%d", before, got)
	}
}

func TestRunStoreKeepsCheckpointHistoryAndLatest(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	for turn := 1; turn <= 3; turn++ {
		runs.saveCheckpoint("task-1", &runCheckpoint{Turn: turn, Transcript: newTranscript("prompt")})
	}
	latest := runs.loadCheckpoint("task-1")
	if latest == nil || latest.Turn != 3 {
		t.Fatalf("latest=%+v, want turn 3", latest)
	}
	history := runs.listCheckpoints("task-1")
	if len(history) != 3 || history[0].Turn != 1 || history[2].Turn != 3 {
		t.Fatalf("history=%+v", history)
	}
	if middle := runs.loadCheckpointTurn("task-1", 2); middle == nil || middle.Turn != 2 {
		t.Fatalf("middle=%+v", middle)
	}
	runs.deleteCheckpoint("task-1")
	if got := runs.listCheckpoints("task-1"); len(got) != 0 {
		t.Fatalf("history after delete=%+v", got)
	}
}

func TestRunStoreFutureCheckpointQuarantinedAndResumeFallsBack(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	runs.saveCheckpoint("task-v", &runCheckpoint{Turn: 1, Transcript: newTranscript("prompt")})
	// Simulate a checkpoint written by a newer binary (e.g. after a downgrade).
	path := filepath.Join(runs.dir, "task-v.ckpt.json")
	future := `{"version": 3, "turn": 9, "transcript": null, "opaque_future_field": true}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	if cp := runs.loadCheckpoint("task-v"); cp != nil {
		t.Fatalf("future checkpoint must not be resumed: %+v", cp)
	}
	moved, err := filepath.Glob(path + ".v3.*.quarantine")
	if err != nil || len(moved) != 1 {
		t.Fatalf("future checkpoint must be quarantined, got %v err=%v", moved, err)
	}
	kept, err := os.ReadFile(moved[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != future {
		t.Fatalf("quarantine must preserve original bytes: %s", kept)
	}
	// With latest quarantined, resume falls back cleanly (fresh start).
	if cp := runs.loadCheckpoint("task-v"); cp != nil {
		t.Fatalf("second load must also fall back, got %+v", cp)
	}
}

func TestRunStoreLoadsLegacyUnstampedTaskAndQuarantinesFuture(t *testing.T) {
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	if err := os.WriteFile(filepath.Join(runs.dir, "legacy.json"), []byte(`{"task_id": "legacy", "status": "completed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs.dir, "future.json"), []byte(`{"version": 2, "task_id": "future", "status": "completed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runs.save(&scheduler.Task{TaskID: "stamped", Status: "completed"})

	got := runs.load()
	loaded := map[string]bool{}
	for _, task := range got {
		loaded[task.TaskID] = true
	}
	if len(got) != 2 || !loaded["legacy"] || !loaded["stamped"] {
		t.Fatalf("load = %+v, want legacy+stamped only", loaded)
	}
	moved, err := filepath.Glob(filepath.Join(runs.dir, "future.json.v2.*.quarantine"))
	if err != nil || len(moved) != 1 {
		t.Fatalf("future task row must be quarantined, got %v err=%v", moved, err)
	}
	raw, err := os.ReadFile(filepath.Join(runs.dir, "stamped.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version": 1`) {
		t.Fatalf("saved task row must be stamped v1: %s", raw)
	}
}
