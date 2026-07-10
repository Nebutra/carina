package daemon

import (
	"strings"
	"testing"
)

func TestTranscriptTruncatesOversizedObservation(t *testing.T) {
	tr := newTranscript("task")
	big := strings.Repeat("x", 5000)
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read big", Obs: Observation{Content: big}})
	if len(tr.Turns[0].Obs.Content) > tr.policy.ToolOutputMax+20 {
		t.Fatalf("observation not truncated: %d", len(tr.Turns[0].Obs.Content))
	}
}

func TestTranscriptCompactionElidesThenSummarizes(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{MaxChars: 800, KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4}
	// Add many turns to blow the budget.
	for i := 0; i < 10; i++ {
		content := strings.Repeat("data ", 60) // ~300 chars each
		pinned := i == 9                       // last one pinned
		tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: content, Pinned: pinned}})
	}
	wantPreimage := compactionPreimageHash(tr.Summary, tr.Turns[:len(tr.Turns)-tr.policy.KeepRecent])
	summarizeCalled := false
	receipt := tr.compact(func(head string) (string, error) {
		summarizeCalled = true
		return "SUMMARY: read many files", nil
	})
	// After compaction the rendered view must be within budget-ish and a
	// summary must exist.
	if !summarizeCalled {
		t.Fatal("summarizer should have been called when over budget")
	}
	if tr.Summary == "" {
		t.Fatal("summary should be set")
	}
	if receipt == nil || receipt.RemovedTurns == 0 || receipt.PreimageSHA256 == "" || receipt.SummarySHA256 == "" {
		t.Fatalf("compaction receipt missing integrity fields: %+v", receipt)
	}
	if receipt.SummarySHA256 != sha256Hex(tr.Summary) {
		t.Fatalf("summary receipt hash does not verify: %+v", receipt)
	}
	if receipt.PreimageSHA256 != wantPreimage {
		t.Fatalf("preimage receipt hash does not verify: %+v", receipt)
	}
	// The most-recent (pinned) turn must survive verbatim.
	last := tr.Turns[len(tr.Turns)-1]
	if last.Obs.Elided {
		t.Fatal("recent/pinned turn must not be elided")
	}
}

func TestLoopGuardRepeatAndStall(t *testing.T) {
	g := newLoopGuard()
	g.MaxRepeat = 3
	// same action 3x -> repeated
	if g.repeated("read", "a.go") {
		t.Fatal("first call should not be flagged")
	}
	g.repeated("read", "a.go")
	if !g.repeated("read", "a.go") {
		t.Fatal("3rd identical action should be flagged as repeated")
	}
	// a different action is independent
	if g.repeated("read", "b.go") {
		t.Fatal("distinct action should not be flagged")
	}

	g2 := newLoopGuard()
	g2.MaxNoProgress = 3
	g2.tick()
	g2.tick()
	if g2.stalled() {
		t.Fatal("not stalled yet")
	}
	g2.tick()
	if !g2.stalled() {
		t.Fatal("should be stalled after MaxNoProgress ticks")
	}
	g2.madeProgress()
	if g2.stalled() {
		t.Fatal("progress should reset the stall counter")
	}
}
