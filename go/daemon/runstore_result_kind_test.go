package daemon

import (
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
)

func TestRunStoreRoundTripsResultKindAndAcceptsLegacyAbsence(t *testing.T) {
	store := newRunStore(t.TempDir())
	run := scheduler.New().SubmitWithGoalModelAgent("sess_1", "ws_1", "hello", "", "plan", nil)
	run.ResultKind = "answer"
	if err := store.saveChecked(run); err != nil {
		t.Fatal(err)
	}
	loaded := store.load()
	if len(loaded) != 1 || loaded[0].ResultKind != "answer" {
		t.Fatalf("loaded runs = %+v", loaded)
	}

	legacy := scheduler.New().Submit("sess_2", "ws_1", "legacy")
	if err := store.saveChecked(legacy); err != nil {
		t.Fatal(err)
	}
	loaded = store.load()
	for _, candidate := range loaded {
		if candidate.RunID == legacy.RunID && candidate.ResultKind != "" {
			t.Fatalf("legacy result kind = %q, want empty", candidate.ResultKind)
		}
	}
}
