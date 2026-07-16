package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func streamEvent(delta string, done bool) map[string]any {
	return map[string]any{
		"type":      "ModelOutputDelta",
		"timestamp": "2026-07-09T10:11:12Z",
		"payload":   map[string]any{"call_id": "c1", "delta": delta, "done": done},
	}
}

// Deltas in, expected stable/tail split out: committed blocks become
// immutable entries under the message header while the mutable tail is one
// keyed entry replaced in place, and the header flips to its terminal state
// on stream end — all without duplicating a single line.
func TestStreamingCommitSequence(t *testing.T) {
	m, _ := newTestModel(nil)

	m.handleEvent(streamEvent("# Title\n\nfirst paragraph\n", false))
	if got := len(m.tr.entries); got != 3 {
		t.Fatalf("entries after first delta = %d, want head+chunk+tail", got)
	}
	got := strings.Join(m.tr.lines, "\n")
	for _, want := range []string{"model", "# Title", "first paragraph"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q:\n%s", want, got)
		}
	}

	// The heading committed; the paragraph is still mutable and grows in place.
	m.handleEvent(streamEvent(" continues\n", false))
	if strings.Count(strings.Join(m.tr.lines, "\n"), "first paragraph") != 1 {
		t.Fatalf("tail must be replaced in place, not appended:\n%s", strings.Join(m.tr.lines, "\n"))
	}

	m.handleEvent(streamEvent("\nlast\n", true))
	if len(m.streams) != 0 {
		t.Fatalf("stream state must be released on done")
	}
	want := []string{
		"10:11:12 + agent model completed",
		"  # Title",
		"  ",
		"  first paragraph continues",
		"  ",
		"  last",
	}
	if !reflect.DeepEqual(m.tr.lines, want) {
		t.Errorf("final transcript = %#v, want %#v", m.tr.lines, want)
	}
}

// An interleaved event lands after the mutable tail, and later commits still
// insert into the message's own slot, keeping the streamed message contiguous.
func TestStreamingKeepsMessageContiguousAcrossInterleavedEvents(t *testing.T) {
	m, _ := newTestModel(nil)
	m.handleEvent(streamEvent("alpha\n\nbeta\n", false))
	m.handleEvent(map[string]any{
		"type":      "CommandExited",
		"timestamp": "2026-07-09T10:11:13Z",
		"payload":   map[string]any{"exit_code": float64(0)},
	})
	m.handleEvent(streamEvent("\ngamma\n", true))
	joined := strings.Join(m.tr.lines, "\n")
	beta, gamma, exit := strings.Index(joined, "beta"), strings.Index(joined, "gamma"), strings.Index(joined, "exit 0")
	if beta < 0 || gamma < 0 || exit < 0 {
		t.Fatalf("transcript incomplete:\n%s", joined)
	}
	if !(beta < gamma && gamma < exit) {
		t.Errorf("streamed message must stay contiguous before the interleaved event:\n%s", joined)
	}
}

// The hard case for contiguity: a delta ends exactly on a block boundary, so
// the tail is momentarily empty and its entry removed. Later commits and the
// done-path flush must still anchor to the message's own last entry — a
// headerless chunk appended after an interleaved event would read as that
// event's body.
func TestStreamingEmptyTailAnchorsCommitsToOwnMessage(t *testing.T) {
	m, _ := newTestModel(nil)
	// Boundary lands exactly after the blank line: the tail entry is removed.
	m.handleEvent(streamEvent("alpha\n\n", false))
	m.handleEvent(map[string]any{
		"type":      "CommandExited",
		"timestamp": "2026-07-09T10:11:13Z",
		"payload":   map[string]any{"exit_code": float64(0)},
	})
	// Both a mid-stream commit and the final Finish flush must insert into the
	// message's slot, not after the command event.
	m.handleEvent(streamEvent("beta\n\n", false))
	m.handleEvent(streamEvent("gamma\n", true))
	joined := strings.Join(m.tr.lines, "\n")
	alpha, beta, gamma, exit := strings.Index(joined, "alpha"), strings.Index(joined, "beta"),
		strings.Index(joined, "gamma"), strings.Index(joined, "exit 0")
	if alpha < 0 || beta < 0 || gamma < 0 || exit < 0 {
		t.Fatalf("transcript incomplete:\n%s", joined)
	}
	if !(alpha < beta && beta < gamma && gamma < exit) {
		t.Errorf("streamed chunks must stay under their own header, before the interleaved event:\n%s", joined)
	}
}

// A mid-line delta leaves the newline-gated tail byte-identical: nothing may
// re-render, the transcript lines stay untouched, and the unseen-lines counter
// (lines, not token deltas) must not inflate while the operator reads history.
func TestStreamingMidLineDeltaIsQuiet(t *testing.T) {
	m, _ := newTestModel(nil)
	m.handleEvent(streamEvent("streamed line\npartial ", false))
	m.followTail = false
	m.unseenLines = 0
	linesBefore := append([]string(nil), m.tr.lines...)
	for i := 0; i < 100; i++ {
		m.handleEvent(streamEvent("x", false))
	}
	if !reflect.DeepEqual(m.tr.lines, linesBefore) {
		t.Errorf("mid-line deltas must not change rendered lines:\n%v\nvs\n%v", m.tr.lines, linesBefore)
	}
	if m.unseenLines != 0 {
		t.Errorf("unseenLines = %d after 100 no-op deltas, want 0", m.unseenLines)
	}
	// The buffered bytes are not lost: they surface once the line completes
	// (soft-wrapped, hence comparing with whitespace collapsed).
	m.handleEvent(streamEvent("\n", true))
	compact := strings.NewReplacer("\n", "", " ", "").Replace(strings.Join(m.tr.lines, "\n"))
	if !strings.Contains(compact, "partial"+strings.Repeat("x", 100)) {
		t.Errorf("buffered mid-line content lost:\n%s", strings.Join(m.tr.lines, "\n"))
	}
	if m.unseenLines < 1 {
		t.Errorf("a real commit is unseen activity, got unseenLines = %d", m.unseenLines)
	}
}

// A held-back table never renders half-committed: it stays in the mutable
// tail (re-laid-out on every delta) and commits whole at stream end.
func TestStreamingTableHoldback(t *testing.T) {
	m, _ := newTestModel(nil)
	m.handleEvent(streamEvent("| a | b |\n|---|---|\n", false))
	m.handleEvent(streamEvent("| 1 | 2 |\n", false))
	if got := strings.Join(m.tr.lines, "\n"); strings.Count(got, "a | b") != 1 {
		t.Fatalf("table header duplicated across commits:\n%s", got)
	}
	m.handleEvent(streamEvent("", true))
	got := strings.Join(m.tr.lines, "\n")
	for _, want := range []string{"a | b", "--+--", "1 | 2"} {
		if strings.Count(got, want) != 1 {
			t.Errorf("final table missing or duplicated %q:\n%s", want, got)
		}
	}
}

// Chain-of-thought stays hidden: a delta marked with a non-final channel is
// dropped before it can reach the transcript.
func TestStreamingDropsNonFinalChannels(t *testing.T) {
	m, _ := newTestModel(nil)
	m.handleEvent(map[string]any{
		"type":      "ModelOutputDelta",
		"timestamp": "2026-07-09T10:11:12Z",
		"payload":   map[string]any{"call_id": "c1", "channel": "thought", "delta": "secret reasoning\n\n", "done": false},
	})
	if len(m.tr.entries) != 0 || len(m.streams) != 0 {
		t.Fatalf("non-final channel must not create transcript state: %v", m.tr.lines)
	}
}

// Inbound deltas cross the sanitize boundary: raw escape bytes never survive
// into the append-only source, and Mono output stays escape-free.
func TestStreamingSanitizesDeltas(t *testing.T) {
	m, _ := newTestModel(nil)
	m.handleEvent(streamEvent("evil \x1b[31mred\x1b[0m \x07text\n\n", true))
	joined := strings.Join(m.tr.lines, "\n")
	if !strings.Contains(joined, "evil red text") {
		t.Errorf("sanitized text lost: %q", joined)
	}
	if strings.Contains(joined, "\x1b") || strings.Contains(joined, "\x07") {
		t.Errorf("escape bytes leaked through the stream: %q", joined)
	}
}

// Resize re-renders committed chunks and the tail from their markdown source
// on the same width path as every other presentation.
func TestStreamingEntriesResizeFromSource(t *testing.T) {
	m, _ := newTestModel(nil)
	m.handleEvent(streamEvent("---\n\nstill streaming\n", false))
	m.tr.resizePresentations(theme.New(theme.Mono), 10)
	joined := strings.Join(m.tr.lines, "\n")
	if !strings.Contains(joined, strings.Repeat("-", 8)) || strings.Contains(joined, strings.Repeat("-", 9)) {
		t.Errorf("thematic break must re-render to exactly the new width:\n%s", joined)
	}
}
