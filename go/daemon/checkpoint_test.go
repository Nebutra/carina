package daemon

import (
	"path/filepath"
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
