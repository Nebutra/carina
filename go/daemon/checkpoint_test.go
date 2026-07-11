package daemon

import (
	"github.com/Nebutra/carina/go/scheduler"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := runs.writeRestoreJournal("task", map[string]any{"pending": []string{"p1"}}); err != nil {
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
