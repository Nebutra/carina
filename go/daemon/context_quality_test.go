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

func TestRequestedReadEvidenceSurvivesRepeatedCompactionAndCheckpointRestore(t *testing.T) {
	const taskText = "Read every file, then report FIRST, MID, and LAST exactly. Do not modify files."
	tr := newTranscript(taskText)
	tr.policy = CompactionPolicy{
		MaxChars: 1200, KeepRecent: 3, ToolOutputMax: 10_000, SummarizeAfter: 6,
		VerbatimUserMaxChars: 800, CollapseOnlyMaxPressure: 1.20,
	}
	tr.CompactionBudget = CompactionBudgetSnapshot{
		PolicyVersion: "test", WindowTokens: 2000, ReserveTokens: 200,
		TriggerTokens: 300, MetadataSource: "test",
	}
	for i := range 52 {
		marker := ""
		switch i {
		case 0:
			marker = "FIRST=ORION-731\n"
		case 25:
			marker = "MID=LYRA-418\n"
		case 51:
			marker = "LAST=VEGA-952\n"
		}
		content := fmt.Sprintf("STEP=%02d NEXT=%02d ", i+1, i+2) + marker + strings.Repeat(fmt.Sprintf("payload-%02d ", i), 40)
		tr.addTurn(Turn{
			Tool: "read", Path: fmt.Sprintf("part-%02d.txt", i), ActionBrief: fmt.Sprintf("read part-%02d.txt", i),
			Obs: Observation{Content: content},
		})
		tr.compact(nil)
	}
	if len(tr.CompactionReceipts) == 0 {
		t.Fatal("52-turn fixture did not compact")
	}
	for _, want := range []string{"FIRST=ORION-731", "MID=LYRA-418", "LAST=VEGA-952"} {
		if !strings.Contains(tr.render(), want) {
			t.Fatalf("compacted transcript lost requested evidence %q:\n%s", want, tr.render())
		}
	}

	raw, err := json.Marshal(&runCheckpoint{Turn: 52, Transcript: tr})
	if err != nil {
		t.Fatal(err)
	}
	restored := decodeRunCheckpoint(raw)
	if restored == nil || restored.Transcript == nil {
		t.Fatal("checkpoint restore failed")
	}
	for i := 52; i < 72; i++ {
		restored.Transcript.addTurn(Turn{
			Tool: "read", Path: fmt.Sprintf("part-%02d.txt", i), ActionBrief: fmt.Sprintf("read part-%02d.txt", i),
			Obs: Observation{Content: strings.Repeat(fmt.Sprintf("payload-%02d ", i), 40)},
		})
		restored.Transcript.compact(nil)
	}
	visible := restored.Transcript.render()
	for _, want := range []string{"FIRST=ORION-731", "MID=LYRA-418", "LAST=VEGA-952"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("checkpoint plus repeated compaction lost requested evidence %q:\n%s", want, visible)
		}
	}
	if len(restored.Transcript.DurableFacts) > maxDurableCompactionFacts {
		t.Fatalf("requested evidence ledger exceeded bound: %+v", restored.Transcript.DurableFacts)
	}
}

func TestRequestedReadEvidenceIgnoresTraversalMetadata(t *testing.T) {
	const task = "Follow each NEXT path and read every step. Report FIRST, MID, and LAST exactly."
	ordinary := Turn{Tool: "read", Obs: Observation{Content: "STEP=17 NEXT=18 payload"}}
	if evidence, ok := requestedReadEvidence(task, ordinary); ok {
		t.Fatalf("incidental traversal metadata consumed a durable slot: %q", evidence)
	}
	first := Turn{Tool: "read", Obs: Observation{Content: "STEP=01 FIRST=ORION-731 NEXT=02 payload"}}
	if evidence, ok := requestedReadEvidence(task, first); !ok || evidence != "FIRST=ORION-731" {
		t.Fatalf("explicit FIRST marker not selected: evidence=%q ok=%v", evidence, ok)
	}
	middle := Turn{Tool: "read", Obs: Observation{Content: "STEP=18 MID=LYRA-418 NEXT=19 payload"}}
	if evidence, ok := requestedReadEvidence(task, middle); !ok || evidence != "MID=LYRA-418" {
		t.Fatalf("explicit MID marker not selected: evidence=%q ok=%v", evidence, ok)
	}
}
