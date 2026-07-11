package daemon

import (
	"fmt"
	"path/filepath"
	"reflect"
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

func TestTranscriptTruncationPreservesTailSignal(t *testing.T) {
	tr := newTranscript("build")
	tr.policy.ToolOutputMax = 160
	var output strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&output, "compiling package %d\n", i)
	}
	output.WriteString("FINAL: tests failed\n")

	tr.addTurn(Turn{ActionBrief: "run tests", Obs: Observation{Content: output.String()}})
	got := tr.Turns[0].Obs.Content
	if len(got) > tr.policy.ToolOutputMax {
		t.Fatalf("preview exceeded byte budget: %d > %d", len(got), tr.policy.ToolOutputMax)
	}
	if !strings.HasPrefix(got, "compiling package 0\n") || !strings.HasSuffix(got, "FINAL: tests failed\n") {
		t.Fatalf("head+tail signal not preserved: %q", got)
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Fatalf("preview did not disclose truncation: %q", got)
	}
}

func TestAddTurnSupersedesStaleReadOfSamePath(t *testing.T) {
	tr := newTranscript("task")
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "v1"}})
	if tr.Turns[0].Obs.Elided {
		t.Fatal("first read of a.go must not start elided")
	}
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "v2"}})
	if !tr.Turns[0].Obs.Elided {
		t.Fatal("earlier read of a.go should be elided once a.go is read again")
	}
	if tr.Turns[0].Obs.OriginalSHA256 != sha256Hex("v1") {
		t.Fatalf("elided turn should carry the original content hash, got %q", tr.Turns[0].Obs.OriginalSHA256)
	}
	if tr.Turns[1].Obs.Elided {
		t.Fatal("the new (latest) read must stay verbatim")
	}
	if tr.Turns[1].Obs.Content != "v2" {
		t.Fatalf("latest read content must be untouched, got %q", tr.Turns[1].Obs.Content)
	}
}

func TestAddTurnStaleReadDedupIsPathScoped(t *testing.T) {
	tr := newTranscript("task")
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "va"}})
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read b.go", Path: "b.go", Obs: Observation{Content: "vb"}})
	if tr.Turns[0].Obs.Elided {
		t.Fatal("reading a different path (b.go) must not elide a.go's read")
	}
	if tr.Turns[1].Obs.Elided {
		t.Fatal("first read of b.go must not start elided")
	}
}

func TestAddTurnStaleReadDedupSkipsPinnedAndNonReadTurns(t *testing.T) {
	tr := newTranscript("task")
	// A pinned read (e.g. explicitly kept for the current investigation) must
	// never be elided, matching compact()'s contract for pinned observations.
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "v1", Pinned: true}})
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "v2"}})
	if tr.Turns[0].Obs.Elided {
		t.Fatal("pinned read must never be elided by stale-read dedup")
	}
	// Turns without a Path (e.g. search/list/patch) must never trip dedup,
	// even if their ActionBrief happens to mention a path-like string.
	tr2 := newTranscript("task")
	tr2.addTurn(Turn{Tool: "search", ActionBrief: "search a.go", Obs: Observation{Content: "match a.go:1"}})
	tr2.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "v1"}})
	if tr2.Turns[0].Obs.Elided {
		t.Fatal("a Path-less turn must never be elided by stale-read dedup")
	}
}

func TestAddTurnStaleReadDedupDoesNotDoubleElide(t *testing.T) {
	tr := newTranscript("task")
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "v1"}})
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "v2"}})
	firstHash := tr.Turns[0].Obs.OriginalSHA256
	// A third read must elide the second (now-stale) read, and must leave the
	// already-elided first turn untouched rather than re-hashing it.
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "v3"}})
	if tr.Turns[0].Obs.OriginalSHA256 != firstHash {
		t.Fatal("already-elided turn must not be re-processed")
	}
	if !tr.Turns[1].Obs.Elided || tr.Turns[1].Obs.OriginalSHA256 != sha256Hex("v2") {
		t.Fatal("second read must be elided once a third read of the same path lands")
	}
	if tr.Turns[2].Obs.Elided {
		t.Fatal("the latest (third) read must stay verbatim")
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
	// Version-2 receipt semantics: the preimage covers the FOLDED head turns
	// only (Tool!="user"); this head is all reads, so folded == whole head.
	var folded []Turn
	for _, turn := range tr.Turns[:len(tr.Turns)-tr.policy.KeepRecent] {
		if turn.Tool != "user" {
			folded = append(folded, turn)
		}
	}
	wantPreimage := compactionPreimageHash(tr.Summary, folded)
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
	if receipt.Version != 2 {
		t.Fatalf("receipt version = %d, want 2 (folded-only preimage semantics)", receipt.Version)
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

// TestCompactionPreservesUserTurnsVerbatim proves compact()'s Step-2
// partition keeps a user-authored steering turn OUT of the summarize fold
// even when it is far older than KeepRecent: the turn survives in t.Turns
// with its content intact, never appears in the summarizer's head input, and
// the v2 receipt records the partition explicitly.
func TestCompactionPreservesUserTurnsVerbatim(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{MaxChars: 800, KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4, VerbatimUserMaxChars: 4000}
	steering := "USER STEERING (incorporate this now): do not use library X"
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read f0", Obs: Observation{Content: strings.Repeat("data ", 60)}})
	tr.addTurn(Turn{Tool: "user", ActionBrief: "steer", Obs: Observation{Content: steering, Pinned: true}})
	for i := 0; i < 8; i++ {
		tr.addTurn(Turn{Tool: "read", ActionBrief: fmt.Sprintf("read f%d", i+1), Obs: Observation{Content: strings.Repeat("data ", 60)}})
	}
	var capturedHead string
	receipt := tr.compact(func(head string) (string, error) {
		capturedHead = head
		return "SUMMARY: read many files", nil
	})
	if receipt == nil || receipt.Version != 2 {
		t.Fatalf("expected a v2 compaction receipt, got %+v", receipt)
	}
	if strings.Contains(capturedHead, "do not use library X") {
		t.Fatalf("user steering must not enter the summarize fold, head:\n%s", capturedHead)
	}
	// 10 turns, KeepRecent=2 -> head is turns 1..8; one user turn kept, 7 folded.
	if receipt.RemovedTurns != 7 {
		t.Fatalf("RemovedTurns = %d, want 7 (folded only, not the whole head)", receipt.RemovedTurns)
	}
	if len(receipt.KeptTurnIndices) != 1 || receipt.KeptTurnIndices[0] != 2 {
		t.Fatalf("KeptTurnIndices = %v, want [2]", receipt.KeptTurnIndices)
	}
	if len(tr.Turns) != 3 {
		t.Fatalf("post-compaction turns = %d, want 3 (1 kept user + 2 tail)", len(tr.Turns))
	}
	got := tr.Turns[0]
	if got.Tool != "user" || got.Obs.Content != steering || got.Obs.Elided {
		t.Fatalf("steering turn must survive verbatim, got %+v", got)
	}
	if got.Index != 2 {
		t.Fatalf("kept turn must retain its original index, got %d", got.Index)
	}
}

// TestCompactionReceiptV2Recompute proves the v2 receipt's integrity fields
// recompute from first principles: PreimageSHA256 over previous-summary + the
// folded (non-user) pre-compaction head turns only, and KeptSHA256 over the
// kept turns exactly as retained.
func TestCompactionReceiptV2Recompute(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{MaxChars: 800, KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4, VerbatimUserMaxChars: 4000}
	tr.addTurn(Turn{Tool: "user", ActionBrief: "steer", Obs: Observation{Content: "USER STEERING: prefer the v2 API", Pinned: true}})
	for i := 0; i < 9; i++ {
		tr.addTurn(Turn{Tool: "read", ActionBrief: fmt.Sprintf("read f%d", i), Obs: Observation{Content: strings.Repeat("data ", 60)}})
	}
	headEnd := len(tr.Turns) - tr.policy.KeepRecent
	var wantFolded []Turn
	var wantKeptIdx []int
	for _, turn := range tr.Turns[:headEnd] {
		if turn.Tool == "user" {
			wantKeptIdx = append(wantKeptIdx, turn.Index)
		} else {
			wantFolded = append(wantFolded, turn)
		}
	}
	wantPreimage := compactionPreimageHash(tr.Summary, wantFolded)
	receipt := tr.compact(func(head string) (string, error) { return "SUMMARY: read many files", nil })
	if receipt == nil || receipt.Version != 2 {
		t.Fatalf("expected a v2 receipt, got %+v", receipt)
	}
	if receipt.PreimageSHA256 != wantPreimage {
		t.Fatalf("v2 preimage must cover folded turns only: %+v", receipt)
	}
	if !reflect.DeepEqual(receipt.KeptTurnIndices, wantKeptIdx) {
		t.Fatalf("KeptTurnIndices = %v, want %v", receipt.KeptTurnIndices, wantKeptIdx)
	}
	keptOut := tr.Turns[:len(tr.Turns)-tr.policy.KeepRecent]
	if receipt.KeptSHA256 != turnsSHA256(keptOut) {
		t.Fatalf("KeptSHA256 must recompute over the kept turns as retained: %+v", receipt)
	}
}

// TestCompactionVerbatimUserBudgetTruncatesThenElides proves the
// VerbatimUserMaxChars cap is spent newest-to-oldest with codex's
// oldest-truncated-first shape: the newest kept turns stay verbatim, the
// first turn to overflow is truncated via artifact.Preview (disclosed
// in-band), and older turns are elided with the same OriginalSHA256 fields
// Step-1 elision uses — bounding growth across repeated compactions.
func TestCompactionVerbatimUserBudgetTruncatesThenElides(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{MaxChars: 800, KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4, VerbatimUserMaxChars: 2500}
	userContent := make([]string, 4)
	for i := range userContent {
		userContent[i] = fmt.Sprintf("USER STEERING %d: ", i) + strings.Repeat("steer ", 165) // ~1000 chars each
		tr.addTurn(Turn{Tool: "user", ActionBrief: "steer", Obs: Observation{Content: userContent[i], Pinned: true}})
	}
	for i := 0; i < 6; i++ {
		tr.addTurn(Turn{Tool: "read", ActionBrief: fmt.Sprintf("read f%d", i), Obs: Observation{Content: strings.Repeat("data ", 60)}})
	}
	receipt := tr.compact(func(head string) (string, error) { return "SUMMARY: read many files", nil })
	if receipt == nil {
		t.Fatal("expected compaction to fire")
	}
	kept := tr.Turns[:4]
	// Newest two kept turns (indices 3, 4) fit the 2500 budget verbatim.
	for i := 2; i <= 3; i++ {
		if kept[i].Obs.Elided || kept[i].Obs.Content != userContent[i] {
			t.Fatalf("kept turn %d must stay verbatim within budget: %+v", i, kept[i].Obs)
		}
	}
	// The next-older turn overflows the remaining budget and is truncated.
	overflow := kept[1].Obs
	if overflow.Elided {
		t.Fatalf("overflow turn must be truncated, not elided: %+v", overflow)
	}
	if len(overflow.Content) > 500 {
		t.Fatalf("overflow turn must be truncated to the remaining budget (500), got %d chars", len(overflow.Content))
	}
	if !strings.Contains(overflow.Content, "omitted") {
		t.Fatalf("truncation must be disclosed in-band: %q", overflow.Content)
	}
	// The oldest kept turn is beyond the budget entirely: elided with the same
	// integrity hash Step-1 elision records.
	oldest := kept[0].Obs
	if !oldest.Elided || oldest.OriginalSHA256 != sha256Hex(userContent[0]) {
		t.Fatalf("oldest kept turn must be elided with the original content hash: %+v", oldest)
	}
}

// TestCompactionAllUserHeadSkipsSummarize proves the empty-fold path fails
// closed: a head made entirely of user turns yields no summarizer call, no
// receipt, and an untouched transcript (Step-1 elision aside).
func TestCompactionAllUserHeadSkipsSummarize(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{MaxChars: 200, KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4, VerbatimUserMaxChars: 100000}
	for i := 0; i < 7; i++ {
		tr.addTurn(Turn{Tool: "user", ActionBrief: "steer", Obs: Observation{Content: strings.Repeat("steer ", 50), Pinned: true}})
	}
	summarizeCalled := false
	receipt := tr.compact(func(head string) (string, error) {
		summarizeCalled = true
		return "SUMMARY", nil
	})
	if summarizeCalled {
		t.Fatal("summarizer must never be called when there is nothing to fold")
	}
	if receipt != nil || len(tr.CompactionReceipts) != 0 {
		t.Fatalf("empty fold must not produce a receipt: %+v", receipt)
	}
	if tr.Summary != "" || len(tr.Turns) != 7 {
		t.Fatalf("empty fold must leave the transcript intact: summary=%q turns=%d", tr.Summary, len(tr.Turns))
	}
}

// TestKeyFilesTopKFrequencyOrdering proves keyFiles is a pure, deterministic
// selector: patch-turn paths counted, ordered by edit count descending with
// first-seen order breaking ties, deduplicated, capped at k, and blind to
// non-patch tools.
func TestKeyFilesTopKFrequencyOrdering(t *testing.T) {
	turns := []Turn{
		{Tool: "patch", ActionBrief: "patch b.go"},
		{Tool: "patch", ActionBrief: "patch a.go"},
		{Tool: "read", ActionBrief: "read z.go"}, // reads never count
		{Tool: "patch", ActionBrief: "patch a.go"},
		{Tool: "patch", ActionBrief: "patch c.go"},
		{Tool: "patch", ActionBrief: "patch c.go"},
		{Tool: "patch", ActionBrief: "patch a.go"},
		{Tool: "patch", ActionBrief: "patch d.go"},
		{Tool: "patch", ActionBrief: "patch e.go"},
		{Tool: "patch", ActionBrief: "patch f.go"},
	}
	got := keyFiles(turns, 5)
	// a.go edited 3x, c.go 2x; b/d/e/f tie at 1 and rank by first-seen.
	want := []string{"a.go", "c.go", "b.go", "d.go", "e.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keyFiles = %v, want %v", got, want)
	}
	if out := keyFiles(turns, 0); out != nil {
		t.Fatalf("k=0 must select nothing, got %v", out)
	}
}

// TestCompactionKeptTurnsSurviveCheckpointRoundTrip proves kept user turns
// and the v2 receipt survive the real checkpoint persistence path
// (saveCheckpointChecked -> JSON on disk -> loadCheckpoint), so a resumed
// task still sees its steering verbatim.
func TestCompactionKeptTurnsSurviveCheckpointRoundTrip(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{MaxChars: 800, KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4, VerbatimUserMaxChars: 4000}
	steering := "USER STEERING (incorporate this now): do not use library X"
	tr.addTurn(Turn{Tool: "user", ActionBrief: "steer", Obs: Observation{Content: steering, Pinned: true}})
	for i := 0; i < 4; i++ {
		tr.addTurn(Turn{Tool: "read", ActionBrief: fmt.Sprintf("read f%d", i), Obs: Observation{Content: strings.Repeat("data ", 60)}})
	}
	// The patch turn sits in the head (all but the KeepRecent tail), so it is
	// folded and must surface in the receipt's KeyFiles.
	tr.addTurn(Turn{Tool: "patch", ActionBrief: "patch f0.go", Obs: Observation{Content: "applied"}})
	for i := 4; i < 8; i++ {
		tr.addTurn(Turn{Tool: "read", ActionBrief: fmt.Sprintf("read f%d", i), Obs: Observation{Content: strings.Repeat("data ", 60)}})
	}
	receipt := tr.compact(func(head string) (string, error) { return "SUMMARY: read many files", nil })
	if receipt == nil {
		t.Fatal("expected compaction to fire")
	}
	runs := newRunStore(filepath.Join(t.TempDir(), "state"))
	if err := runs.saveCheckpointChecked("task-verbatim", &runCheckpoint{Turn: 5, Transcript: tr}); err != nil {
		t.Fatalf("saveCheckpointChecked: %v", err)
	}
	loaded := runs.loadCheckpoint("task-verbatim")
	if loaded == nil || loaded.Transcript == nil {
		t.Fatal("checkpoint did not round-trip")
	}
	got := loaded.Transcript.Turns[0]
	if got.Tool != "user" || got.Obs.Content != steering || !got.Obs.Pinned || got.Index != 1 {
		t.Fatalf("kept user turn must survive checkpoint round-trip verbatim, got %+v", got)
	}
	if len(loaded.Transcript.CompactionReceipts) != 1 {
		t.Fatalf("receipts must round-trip, got %d", len(loaded.Transcript.CompactionReceipts))
	}
	gotReceipt := loaded.Transcript.CompactionReceipts[0]
	if gotReceipt.Version != 2 || !reflect.DeepEqual(gotReceipt.KeptTurnIndices, []int{1}) {
		t.Fatalf("v2 receipt fields must round-trip: %+v", gotReceipt)
	}
	if !reflect.DeepEqual(gotReceipt.KeyFiles, []string{"f0.go"}) {
		t.Fatalf("KeyFiles must round-trip: %+v", gotReceipt.KeyFiles)
	}
	keptOut := loaded.Transcript.Turns[:len(loaded.Transcript.Turns)-tr.policy.KeepRecent]
	if gotReceipt.KeptSHA256 != turnsSHA256(keptOut) {
		t.Fatalf("KeptSHA256 must verify against the reloaded kept turns: %+v", gotReceipt)
	}
}

// TestRenderSummaryTemplateOmitsEmptySections proves renderSummaryTemplate
// never emits a dangling heading (e.g. "Blocked:") when that section has no
// items — a compaction summary for a task with nothing blocked should not
// carry a bullet-less "Blocked:" line.
func TestRenderSummaryTemplateOmitsEmptySections(t *testing.T) {
	sc := SummaryContent{
		Goal: "fix the bug",
		Done: []string{"reproduced the failure"},
		Next: []string{"patch the root cause"},
	}
	got := renderSummaryTemplate(sc)
	if !strings.Contains(got, "Goal: fix the bug") {
		t.Fatalf("missing goal line: %q", got)
	}
	if !strings.Contains(got, "Done:\n- reproduced the failure") {
		t.Fatalf("missing done section: %q", got)
	}
	if !strings.Contains(got, "Next:\n- patch the root cause") {
		t.Fatalf("missing next section: %q", got)
	}
	for _, heading := range []string{headingInProgress, headingBlocked, headingHighlights, headingFilesRead, headingFilesMod} {
		if strings.Contains(got, heading) {
			t.Fatalf("empty section %q must be omitted entirely, got:\n%s", heading, got)
		}
	}
}

// TestParseSummaryContentRoundTripsRenderedTemplate proves render+parse
// compose: a SummaryContent rendered by renderSummaryTemplate must parse back
// into an equivalent SummaryContent, so a caller can recover structure from
// an already-compacted Transcript.Summary.
func TestParseSummaryContentRoundTripsRenderedTemplate(t *testing.T) {
	sc := SummaryContent{
		Goal:          "ship the feature",
		Done:          []string{"wrote the parser", "wrote the renderer"},
		InProgress:    []string{"wiring into agent.go"},
		Blocked:       []string{"waiting on kernel review"},
		Highlights:    []string{"chose deterministic Files extraction over model recall"},
		Next:          []string{"add tests"},
		FilesRead:     []string{"go/daemon/transcript.go"},
		FilesModified: []string{"go/daemon/agent.go"},
	}
	rendered := renderSummaryTemplate(sc)
	got, ok := parseSummaryContent(rendered)
	if !ok {
		t.Fatalf("parse failed on renderer's own output:\n%s", rendered)
	}
	if got.Goal != sc.Goal {
		t.Fatalf("Goal mismatch: got %q want %q", got.Goal, sc.Goal)
	}
	for _, pair := range []struct {
		name      string
		got, want []string
	}{
		{"Done", got.Done, sc.Done},
		{"InProgress", got.InProgress, sc.InProgress},
		{"Blocked", got.Blocked, sc.Blocked},
		{"Highlights", got.Highlights, sc.Highlights},
		{"Next", got.Next, sc.Next},
		{"FilesRead", got.FilesRead, sc.FilesRead},
		{"FilesModified", got.FilesModified, sc.FilesModified},
	} {
		if strings.Join(pair.got, "|") != strings.Join(pair.want, "|") {
			t.Fatalf("%s mismatch: got %v want %v", pair.name, pair.got, pair.want)
		}
	}
}

// TestParseSummaryContentFailsClosedOnUnstructuredProse proves the parser
// does not fabricate structure from plain prose (e.g. a model that ignored
// the structured-prompt instruction and replied with free text, or a
// pre-template Transcript.Summary from before this change) — callers must
// treat text without a recognizable "Goal:" heading as opaque prose, exactly
// as compact() did before this template existed.
func TestParseSummaryContentFailsClosedOnUnstructuredProse(t *testing.T) {
	_, ok := parseSummaryContent("Fixed the bug in the parser and added tests.")
	if ok {
		t.Fatal("unstructured prose without a Goal: heading must not parse as structured")
	}
}

// TestFilesTouchedDerivesFromTranscriptNotModel proves the Files(read+
// modified) section is computed deterministically from the transcript's own
// turns (ActionBrief "read <path>" / "patch <path>", set by briefAction),
// deduplicated in first-seen order, and untouched by non-read/patch tools.
func TestFilesTouchedDerivesFromTranscriptNotModel(t *testing.T) {
	turns := []Turn{
		{Tool: "read", ActionBrief: "read a.go"},
		{Tool: "run", ActionBrief: "run [go test]"},
		{Tool: "read", ActionBrief: "read b.go"},
		{Tool: "patch", ActionBrief: "patch a.go"},
		{Tool: "read", ActionBrief: "read a.go"},   // re-read: must not duplicate
		{Tool: "patch", ActionBrief: "patch a.go"}, // re-patch: must not duplicate
	}
	read, modified := filesTouched(turns)
	if strings.Join(read, ",") != "a.go,b.go" {
		t.Fatalf("FilesRead = %v, want [a.go b.go]", read)
	}
	if strings.Join(modified, ",") != "a.go" {
		t.Fatalf("FilesModified = %v, want [a.go]", modified)
	}
}

// TestFilesTouchedCapsAtMaxFiles proves a long-running task's Files sections
// cannot grow unboundedly with the rest of the compaction summary.
func TestFilesTouchedCapsAtMaxFiles(t *testing.T) {
	var turns []Turn
	for i := 0; i < 30; i++ {
		turns = append(turns, Turn{Tool: "read", ActionBrief: fmt.Sprintf("read f%d.go", i)})
	}
	read, _ := filesTouched(turns)
	if len(read) != 20 {
		t.Fatalf("FilesRead should cap at 20, got %d", len(read))
	}
}

// TestCompactionSummaryUsesStructuredTemplateEndToEnd drives compact() with a
// summarizer shaped like agent.go's runLoopContext closure (structured
// prompt -> parseSummaryContent -> deterministic Files -> renderSummaryTemplate)
// to prove the whole composition survives being called through compact(),
// including landing in the CompactionReceipt's SummarySHA256.
func TestCompactionSummaryUsesStructuredTemplateEndToEnd(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{MaxChars: 800, KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4}
	for i := 0; i < 8; i++ {
		content := strings.Repeat("data ", 60)
		tr.addTurn(Turn{Tool: "read", ActionBrief: fmt.Sprintf("read f%d.go", i), Obs: Observation{Content: content}})
	}
	tr.addTurn(Turn{Tool: "patch", ActionBrief: "patch f0.go", Obs: Observation{Content: "applied", Pinned: true}})
	for i := 0; i < 3; i++ {
		content := strings.Repeat("data ", 60)
		tr.addTurn(Turn{Tool: "read", ActionBrief: fmt.Sprintf("read g%d.go", i), Obs: Observation{Content: content}})
	}

	summarizeLikeAgent := func(head string) (string, error) {
		modelReply := "Goal: fix the bug\nDone:\n- read the source files\nNext:\n- verify the patch\n"
		sc, ok := parseSummaryContent(modelReply)
		if !ok {
			return modelReply, nil
		}
		sc.FilesRead, sc.FilesModified = filesTouched(tr.Turns)
		return renderSummaryTemplate(sc), nil
	}
	receipt := tr.compact(summarizeLikeAgent)
	if receipt == nil {
		t.Fatal("expected compaction to fire")
	}
	if !strings.HasPrefix(tr.Summary, "Goal: fix the bug") {
		t.Fatalf("summary should start with the structured Goal line, got:\n%s", tr.Summary)
	}
	if !strings.Contains(tr.Summary, "Files Read:") || !strings.Contains(tr.Summary, "f0.go") {
		t.Fatalf("summary should carry a deterministic Files Read section, got:\n%s", tr.Summary)
	}
	if !strings.Contains(tr.Summary, "Files Modified:\n- f0.go") {
		t.Fatalf("summary should carry a deterministic Files Modified section, got:\n%s", tr.Summary)
	}
	if receipt.SummarySHA256 != sha256Hex(tr.Summary) {
		t.Fatalf("compaction receipt hash must verify against the structured summary")
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

// TestShouldCompactMaxTokensZeroMatchesCharTriggerOnly is the byte-identical
// regression test for MaxTokens' default: with MaxTokens unset (zero), the
// combiner must reduce to exactly the pre-existing t.size() > triggerChars()
// check, both below and above the char trigger.
func TestShouldCompactMaxTokensZeroMatchesCharTriggerOnly(t *testing.T) {
	tr := newTranscript("task")
	tr.policy = CompactionPolicy{MaxChars: 800, KeepRecent: 2, ToolOutputMax: 400, SummarizeAfter: 4}
	if tr.shouldCompact() {
		t.Fatal("empty transcript under the char budget must not trigger compaction")
	}
	for i := 0; i < 10; i++ {
		content := strings.Repeat("data ", 60) // ~300 chars each
		tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: content}})
	}
	if tr.size() <= tr.policy.MaxChars {
		t.Fatalf("fixture must exceed MaxChars to prove the char-trigger side of the OR: size=%d", tr.size())
	}
	if !tr.shouldCompact() {
		t.Fatal("over-budget transcript must trigger compaction via the char trigger alone")
	}
}

// TestShouldCompactMaxTokensFiresBelowCharTrigger proves the token-estimate
// side of the OR can fire compaction even while comfortably under the char
// budget — the whole point of a token co-trigger being additive, not a
// replacement for the char trigger.
func TestShouldCompactMaxTokensFiresBelowCharTrigger(t *testing.T) {
	tr := newTranscript("task")
	tr.policy = CompactionPolicy{MaxChars: 1_000_000, KeepRecent: 2, ToolOutputMax: 100_000, SummarizeAfter: 4}
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: strings.Repeat("data ", 60)}})
	if tr.size() >= tr.policy.MaxChars {
		t.Fatalf("fixture must stay well under MaxChars: size=%d", tr.size())
	}
	if tr.shouldCompact() {
		t.Fatal("MaxTokens=0 (unset) must never trigger compaction on its own")
	}
	tr.policy.MaxTokens = estimateTokens(tr.render()) - 1
	if !tr.shouldCompact() {
		t.Fatal("MaxTokens set below the current estimated token count must trigger compaction even though the char budget is not exceeded")
	}
}

// TestShouldCompactMaxTokensAboveCurrentDoesNotFire confirms MaxTokens only
// contributes a positive trigger, never suppresses the (still-unmet) char
// trigger nor fires early when the estimate is comfortably under it.
func TestShouldCompactMaxTokensAboveCurrentDoesNotFire(t *testing.T) {
	tr := newTranscript("task")
	tr.policy = CompactionPolicy{MaxChars: 1_000_000, KeepRecent: 2, ToolOutputMax: 100_000, SummarizeAfter: 4, MaxTokens: 1_000_000}
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: strings.Repeat("data ", 60)}})
	if tr.shouldCompact() {
		t.Fatal("MaxTokens far above the current estimate must not trigger compaction")
	}
}

// TestCompactFiresOnTokenTriggerAlone drives compact() itself (not just
// shouldCompact) through the token-estimate trigger while MaxChars is set so
// high the char trigger never fires, proving both of compact()'s gates read
// the combiner rather than the old size()<=triggerChars() checks.
func TestCompactFiresOnTokenTriggerAlone(t *testing.T) {
	tr := newTranscript("fix the bug")
	tr.policy = CompactionPolicy{
		MaxChars: 1_000_000, KeepRecent: 2, ToolOutputMax: 100_000, SummarizeAfter: 4,
	}
	for i := 0; i < 10; i++ {
		content := strings.Repeat("data ", 60) // ~300 chars each
		tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: content}})
	}
	if tr.size() >= tr.policy.MaxChars {
		t.Fatalf("fixture must stay well under MaxChars — the whole point is proving MaxTokens, not MaxChars, drives compaction: size=%d", tr.size())
	}
	// compact()'s second (summarize-decision) gate re-checks shouldCompact()
	// AFTER step 1 has already elided the head turns, which shrinks the
	// rendered estimate substantially (elided turns render as a short
	// placeholder). MaxTokens must stay below that post-elision estimate, not
	// just the pre-elision one, so the token trigger — not MaxChars, which
	// stays unmet throughout — is what carries compact() through both gates.
	cutoff := len(tr.Turns) - tr.policy.KeepRecent
	postElisionTurns := append([]Turn(nil), tr.Turns...)
	for i := 0; i < cutoff; i++ {
		postElisionTurns[i].Obs.Elided = true
	}
	postElisionTranscript := &Transcript{Task: tr.Task, Summary: tr.Summary, Turns: postElisionTurns}
	tr.policy.MaxTokens = estimateTokens(postElisionTranscript.render()) - 1
	summarizeCalled := false
	receipt := tr.compact(func(head string) (string, error) {
		summarizeCalled = true
		return "SUMMARY: read many files", nil
	})
	if !summarizeCalled || receipt == nil {
		t.Fatalf("compact() must fire off the token-estimate trigger alone when the char trigger is unmet (summarizeCalled=%v receipt=%v)", summarizeCalled, receipt)
	}
}

func TestSummaryTemplateRoundTripAndOmissions(t *testing.T) {
	want := SummaryContent{
		Goal:          "ship the runtime",
		Done:          []string{"closed lifecycle gaps"},
		InProgress:    []string{"validate release"},
		Highlights:    []string{"raw cursors are durable"},
		Next:          []string{"publish the tag"},
		FilesRead:     []string{"README.md"},
		FilesModified: []string{"go/daemon/agent.go"},
	}
	rendered := renderSummaryTemplate(want)
	if strings.Contains(rendered, headingBlocked) {
		t.Fatalf("empty blocked section was rendered: %q", rendered)
	}
	got, ok := parseSummaryContent(rendered)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, %v; want %#v", got, ok, want)
	}
	if _, ok := parseSummaryContent("unstructured prose"); ok {
		t.Fatal("unstructured summary was accepted as structured")
	}
}

func TestFilesTouchedIsGroundedDeduplicatedAndBounded(t *testing.T) {
	turns := []Turn{
		{Tool: "read", ActionBrief: "read README.md"},
		{Tool: "read", ActionBrief: "read README.md"},
		{Tool: "patch", ActionBrief: "patch go/daemon/agent.go"},
		{Tool: "search", ActionBrief: "search ignored"},
	}
	for i := 0; i < 25; i++ {
		turns = append(turns, Turn{Tool: "read", ActionBrief: fmt.Sprintf("read file-%02d", i)})
	}
	read, modified := filesTouched(turns)
	if len(read) != 20 || read[0] != "README.md" {
		t.Fatalf("read files = %#v", read)
	}
	if !reflect.DeepEqual(modified, []string{"go/daemon/agent.go"}) {
		t.Fatalf("modified files = %#v", modified)
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
