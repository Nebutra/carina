package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMemorySnapshotForPromptRanksRelevantEntriesWithinBudget(t *testing.T) {
	store := newMemoryStore(t.TempDir())
	scope := memoryScope{Profile: "test", WorkspaceRoot: t.TempDir()}
	for _, entry := range []string{"parser style uses tabs", "billing release checklist", "unrelated historical note"} {
		if _, err := store.apply(scope, memoryWriteRequest{Action: "add", Target: memoryTargetMemory, Content: entry}); err != nil {
			t.Fatal(err)
		}
	}
	got := store.snapshotForPrompt(scope, "billing release", 2000)
	if !strings.Contains(got, "billing release checklist") {
		t.Fatalf("relevant memory missing: %s", got)
	}
	if strings.Index(got, "billing release checklist") > strings.Index(got, "parser style uses tabs") {
		t.Fatalf("relevant memory was not ranked first: %s", got)
	}
	if len(got) > 2000+len("\n…[memory snapshot truncated]") {
		t.Fatalf("memory snapshot exceeded hard budget: %d", len(got))
	}
}

func TestSemanticCompactionDetectsTopicShiftButNotContinuation(t *testing.T) {
	shift := newTranscript("task")
	shift.policy.MaxTokens = 100000
	shift.policy.MaxChars = 1 << 20
	shift.policy.LookaheadTokens = 0
	shift.policy.SemanticCompaction = true
	shift.policy.KeepRecent = 1
	shift.addTurn(Turn{Tool: "user", Obs: Observation{Content: "fix parser error handling in the compiler"}})
	shift.addTurn(Turn{Tool: "read", ActionBrief: "read parser", Obs: Observation{Content: "evidence"}})
	shift.addTurn(Turn{Tool: "user", ActionBrief: "user-followup", Obs: Observation{Content: "prepare the billing deployment checklist and release notes"}})
	if !shift.semanticShiftPending || !shift.shouldCompact() {
		t.Fatalf("topic shift should trigger semantic compaction: pending=%v", shift.semanticShiftPending)
	}

	continuation := newTranscript("task")
	continuation.policy = shift.policy
	continuation.addTurn(Turn{Tool: "user", Obs: Observation{Content: "fix parser error handling in the compiler"}})
	continuation.addTurn(Turn{Tool: "read", ActionBrief: "read parser", Obs: Observation{Content: "evidence"}})
	continuation.addTurn(Turn{Tool: "user", ActionBrief: "user-followup", Obs: Observation{Content: "continue fixing parser error handling in the compiler"}})
	if continuation.semanticShiftPending {
		t.Fatalf("continuation with strong term overlap should not trigger semantic compaction")
	}
}

func TestSixtyTurnCompactionPreservesTaskSteeringFailuresAndChangedPaths(t *testing.T) {
	const taskText = "repair billing without changing the public v1 contract"
	tr := newTranscript(taskText)
	tr.policy = CompactionPolicy{
		MaxChars: 1400, KeepRecent: 3, ToolOutputMax: 220, SummarizeAfter: 5,
		VerbatimUserMaxChars: 800, CollapseOnlyMaxPressure: 1.20,
	}
	receipts := 0
	for i := range 60 {
		turn := Turn{Tool: "read", ActionBrief: fmt.Sprintf("read evidence-%02d.txt", i), Path: fmt.Sprintf("evidence-%02d.txt", i), Obs: Observation{Content: strings.Repeat(fmt.Sprintf("evidence-%02d ", i), 30)}}
		switch i {
		case 3:
			turn = Turn{Tool: "user", ActionBrief: "steer:contract", Obs: Observation{Content: "Keep the public v1 API unchanged.", Pinned: true}}
		case 8:
			turn = Turn{Tool: "run", ActionBrief: "run go test ./billing", Obs: Observation{Content: "failed TestPayment: expected 7, got 8", Pinned: true}}
		case 12:
			turn = Turn{Tool: "edit", ActionBrief: "edit billing/service.go", Obs: Observation{Content: "applied edit to billing/service.go", Pinned: true}}
		}
		tr.addTurn(turn)
		if tr.compact(nil) != nil {
			receipts++
		}
	}
	if receipts < 2 {
		t.Fatalf("60-turn fixture did not exercise repeated compaction: receipts=%d", receipts)
	}
	visible := tr.render()
	for _, want := range []string{"Keep the public v1 API unchanged.", "failed TestPayment", "billing/service.go", "read evidence-59.txt"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("compacted model view lost %q:\n%s", want, visible)
		}
	}
	seg := buildPromptSegments("SYSTEM", taskText, visible, "GO")
	if !strings.Contains(seg.VolatileSuffix, "TASK: "+taskText) {
		t.Fatalf("task disappeared from the post-compact prompt: %s", seg.VolatileSuffix)
	}
	if len(tr.DurableFacts) == 0 || len(tr.DurableFacts) > maxDurableCompactionFacts {
		t.Fatalf("durable fact ledger is not bounded: %+v", tr.DurableFacts)
	}

	// Checkpoint JSON is the restart boundary: deterministic facts must survive
	// process loss, not merely remain in private in-memory fields.
	raw, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	var restored Transcript
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	restoredVisible := restored.render()
	if !strings.Contains(restoredVisible, "failed TestPayment") || !strings.Contains(restoredVisible, "billing/service.go") {
		t.Fatalf("checkpoint restart lost durable facts: %s", restoredVisible)
	}
}
