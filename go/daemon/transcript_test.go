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

func TestTriggerCharsDefaultsToMaxChars(t *testing.T) {
	tr := newTranscript("task")
	tr.policy = CompactionPolicy{MaxChars: 24000}
	if got := tr.triggerChars(); got != 24000 {
		t.Fatalf("zero ReserveChars/ThresholdRatio must reduce to MaxChars, got %d", got)
	}
}

func TestTriggerCharsDualBound(t *testing.T) {
	cases := []struct {
		name           string
		maxChars       int
		reserveChars   int
		thresholdRatio float64
		want           int
	}{
		// Large window: a small fixed reserve leaves more usable room than a
		// proportional cut would, so the reserve-based bound wins.
		{"large window reserve wins", 100000, 2000, 0.9, 98000},
		// Small window: the fixed reserve alone would trigger overly early
		// (6000/8000 = 75%), so the more generous ratio-based bound wins.
		{"small window ratio wins", 8000, 2000, 0.9, 7200},
		// Reserve larger than MaxChars must floor at zero, never negative.
		{"reserve exceeds max floors at zero", 1000, 5000, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := newTranscript("task")
			tr.policy = CompactionPolicy{MaxChars: c.maxChars, ReserveChars: c.reserveChars, ThresholdRatio: c.thresholdRatio}
			if got := tr.triggerChars(); got != c.want {
				t.Fatalf("triggerChars() = %d, want %d", got, c.want)
			}
		})
	}
}

// TestCompactUsesConsistentTriggerAcrossBothGates is the regression test for
// the bug the dual-threshold review caught: compact() used to compare
// t.size() against t.policy.MaxChars independently at two call sites, so a
// lowered effective trigger could apply to the elide gate while the
// summarize-decision gate silently kept using the old, higher MaxChars. This
// reuses the exact turn/content shape already proven (in
// TestTranscriptCompactionElidesThenSummarizes) to drive both gates when
// MaxChars=800, but reaches that same effective trigger via ReserveChars/
// ThresholdRatio with MaxChars set two orders of magnitude higher — so this
// only passes if BOTH gates are reading the computed trigger (800) instead
// of the much larger raw MaxChars (100000), which the pre-fix code would
// have used at the second (summarize-decision) gate.
func TestCompactUsesConsistentTriggerAcrossBothGates(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{
		// trigger = max(100000-99200, 100000*0.008) = max(800, 800) = 800.
		MaxChars: 100000, ReserveChars: 99200, ThresholdRatio: 0.008,
		KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4,
	}
	if got := tr.triggerChars(); got != 800 {
		t.Fatalf("fixture assumption broken: triggerChars() = %d, want 800", got)
	}
	for i := 0; i < 10; i++ {
		content := strings.Repeat("data ", 60) // ~300 chars each
		tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: content}})
	}
	if tr.size() <= 800 {
		t.Fatalf("test fixture must exceed the lowered trigger (800) for compact() to proceed at all: size=%d", tr.size())
	}
	if tr.size() >= tr.policy.MaxChars {
		t.Fatalf("test fixture must stay well under the raw MaxChars (%d) — the whole point is proving the lowered trigger, not MaxChars, drives compaction: size=%d", tr.policy.MaxChars, tr.size())
	}
	summarizeCalled := false
	receipt := tr.compact(func(head string) (string, error) {
		summarizeCalled = true
		return "SUMMARY: read many files", nil
	})
	if !summarizeCalled || receipt == nil {
		t.Fatalf("summarize gate must fire off the same lowered trigger as the elide gate, not stale MaxChars semantics (summarizeCalled=%v receipt=%v)", summarizeCalled, receipt)
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

func TestLoopGuardHardRepeatedTripsOnSingleSignature(t *testing.T) {
	g := newLoopGuard()
	g.MaxHardRepeat = 4
	// The same signature repeated: mistakes only start accumulating once a
	// signature has been seen more than once (the first occurrence is not a
	// mistake — it's the original attempt).
	if _, hard := g.observe("read", "a.go"); hard {
		t.Fatal("first occurrence must not count as a mistake")
	}
	if g.hardStop() {
		t.Fatal("should not be hard-stopped yet")
	}
	for i := 0; i < 4; i++ {
		g.observe("read", "a.go")
	}
	if !g.hardStop() {
		t.Fatalf("should be hard-stopped after %d mistakes, got mistakes=%d", g.MaxHardRepeat, g.mistakes)
	}
}

func TestLoopGuardHardRepeatedTripsAcrossRotatingSignatures(t *testing.T) {
	// A model that rotates between a few repeated actions (never hitting the
	// per-signature MaxRepeat threshold) must still trip the hard stop, since
	// the mistake counter is cumulative across all signatures, not per-key.
	g := newLoopGuard()
	g.MaxRepeat = 10 // high enough that no single signature trips repeated()
	g.MaxHardRepeat = 4
	sigs := []string{"a.go", "b.go", "c.go"}
	tripped := false
	for round := 0; round < 3 && !tripped; round++ {
		for _, s := range sigs {
			if _, hard := g.observe("read", s); hard {
				tripped = true
				break
			}
		}
	}
	if !tripped {
		t.Fatalf("rotating between repeated signatures should still trip the hard stop; mistakes=%d", g.mistakes)
	}
}

func TestLoopGuardHardRepeatDisabledWhenZero(t *testing.T) {
	g := newLoopGuard()
	g.MaxHardRepeat = 0
	for i := 0; i < 20; i++ {
		if _, hard := g.observe("read", "a.go"); hard {
			t.Fatal("MaxHardRepeat=0 must disable the hard stop, not trip immediately")
		}
	}
}

func TestActionSignatureIncludesBatchPayloadButNotThought(t *testing.T) {
	first := action{Thought: "one", Actions: []action{{Tool: "read", Path: "a.go"}}}
	second := action{Thought: "two", Actions: []action{{Tool: "read", Path: "b.go"}}}
	if first.signature() == second.signature() {
		t.Fatal("different batch payloads must have different signatures")
	}
	second.Actions[0].Path = "a.go"
	if first.signature() != second.signature() {
		t.Fatal("thought text must not affect the action signature")
	}
}
