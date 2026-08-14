package daemon

import (
	"errors"
	"strings"
	"testing"
)

func TestAssistantTailRequiresResetOneAndStrictSequence(t *testing.T) {
	var registry assistantTailRegistry
	key := assistantTailKey{sessionID: "session", runID: "run", phase: assistantPhaseFinalAnswer}
	owner, err := registry.begin(key)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		kind       string
		generation uint64
		sequence   uint64
	}{
		{kind: "delta", generation: 1, sequence: 1},
		{kind: "completed", generation: 1, sequence: 1},
		{kind: "reset", generation: 1, sequence: 2},
	}
	for _, update := range invalid {
		if err := registry.publish(owner, update.kind, update.generation, update.sequence, "x", false); !errors.Is(err, errAssistantTailTransition) {
			t.Fatalf("invalid first update %+v = %v", update, err)
		}
	}
	if err := registry.publish(owner, "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	if err := registry.publish(owner, "delta", 1, 3, "gap", false); !errors.Is(err, errAssistantTailTransition) {
		t.Fatalf("gap = %v", err)
	}
	if err := registry.publish(owner, "delta", 2, 1, "no reset", false); !errors.Is(err, errAssistantTailTransition) {
		t.Fatalf("higher generation delta = %v", err)
	}
	if err := registry.publish(owner, "reset", 2, 1, "", false); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantTailBuildsSnapshotsAndAwaitingCanonical(t *testing.T) {
	var registry assistantTailRegistry
	owner, err := registry.begin(assistantTailKey{sessionID: "session", runID: "run", phase: assistantPhaseFinalAnswer})
	if err != nil {
		t.Fatal(err)
	}
	for _, update := range []struct {
		kind     string
		sequence uint64
		text     string
	}{
		{kind: "reset", sequence: 1},
		{kind: "delta", sequence: 2, text: "hel"},
		{kind: "delta", sequence: 3, text: "lo"},
		{kind: "completed", sequence: 4, text: "hello"},
	} {
		if err := registry.publish(owner, update.kind, 1, update.sequence, update.text, false); err != nil {
			t.Fatalf("%s: %v", update.kind, err)
		}
	}
	snapshots, revision := registry.capture("session")
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.Content != "hello" || snapshot.Sequence != 4 || snapshot.State != assistantTailAwaitingCanonical || revision != snapshot.Revision {
		t.Fatalf("snapshot = %+v revision=%d", snapshot, revision)
	}
	if err := registry.publish(owner, "delta", 1, 5, "late", false); !errors.Is(err, errAssistantTailTransition) {
		t.Fatalf("late delta = %v", err)
	}
}

func TestAssistantTailOwnerRevocationPreventsResurrection(t *testing.T) {
	var registry assistantTailRegistry
	key := assistantTailKey{sessionID: "session", runID: "run", phase: assistantPhaseFinalAnswer}
	first, err := registry.begin(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.publish(first, "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	second, err := registry.begin(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.publish(first, "delta", 1, 2, "late", false); !errors.Is(err, errAssistantTailOwner) {
		t.Fatalf("stale owner = %v", err)
	}
	if err := registry.publish(second, "reset", 2, 1, "", false); err != nil {
		t.Fatal(err)
	}
	snapshots, _ := registry.capture("session")
	if len(snapshots) != 1 || snapshots[0].Generation != 3 {
		t.Fatalf("resumed generation did not advance from prior owner: %+v", snapshots)
	}
	if !registry.revoke(second) || registry.revoke(second) {
		t.Fatal("owner revocation was not single-use")
	}
}

func TestAssistantTailCaptureIsRevisionOrdered(t *testing.T) {
	var registry assistantTailRegistry
	for _, runID := range []string{"z", "a", "m"} {
		owner, err := registry.begin(assistantTailKey{sessionID: "session", runID: runID, phase: assistantPhaseFinalAnswer})
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.publish(owner, "reset", 1, 1, "", false); err != nil {
			t.Fatal(err)
		}
	}
	for iteration := 0; iteration < 200; iteration++ {
		snapshots, _ := registry.capture("session")
		if len(snapshots) != 3 || snapshots[0].RunID != "z" || snapshots[1].RunID != "a" || snapshots[2].RunID != "m" {
			t.Fatalf("iteration %d snapshots = %+v", iteration, snapshots)
		}
	}
}

func TestAssistantTailBoundsFailWithoutTruncation(t *testing.T) {
	var registry assistantTailRegistry
	owner, err := registry.begin(assistantTailKey{sessionID: "session", runID: "run", phase: assistantPhaseFinalAnswer})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.publish(owner, "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	tooLarge := strings.Repeat("x", maxProviderResponseBytes+1)
	if err := registry.publish(owner, "delta", 1, 2, tooLarge, false); !errors.Is(err, errAssistantTailOverflow) {
		t.Fatalf("oversized body = %v", err)
	}
	snapshots, _ := registry.capture("session")
	if len(snapshots) != 0 {
		t.Fatalf("overflow retained an unsafe replacement: %+v", snapshots)
	}
	if err := registry.publish(owner, "delta", 1, 2, "late", false); !errors.Is(err, errAssistantTailOwner) {
		t.Fatalf("overflow did not revoke owner: %v", err)
	}

	for index := 0; index < assistantTailMaxSessionEntries; index++ {
		if _, err := registry.begin(assistantTailKey{sessionID: "session", runID: string(rune('a' + index)), phase: assistantPhaseFinalAnswer}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := registry.begin(assistantTailKey{sessionID: "session", runID: "overflow", phase: assistantPhaseFinalAnswer}); !errors.Is(err, errAssistantTailOverflow) {
		t.Fatalf("entry overflow = %v", err)
	}
}
