package tui

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestTranscriptCachesRenderedEntries(t *testing.T) {
	var tr transcript
	tr.push("one")
	tr.push("two\nthree") // multi-line renders flatten into viewport lines
	if len(tr.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(tr.entries))
	}
	if len(tr.lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(tr.lines))
	}
	if tr.entries[1].rendered != "two\nthree" {
		t.Errorf("entry cache lost content: %q", tr.entries[1].rendered)
	}
}

func TestRenderEventGenericCommandLine(t *testing.T) {
	th := theme.New(theme.Mono)
	line := renderEvent(map[string]any{
		"type":      "CommandExecuted",
		"timestamp": "2026-07-09T10:11:12Z",
		"payload":   map[string]any{"command": "echo hi"},
	}, th, "en")
	for _, want := range []string{"10:11:12", "CommandExecuted", "echo hi"} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %q: %q", want, line)
		}
	}
}

// permission.request events render through the Governed register — full
// sentences, exact nouns, the decision_id verbatim.
func TestRenderEventPermissionRequestUsesGovernedRegister(t *testing.T) {
	ev := map[string]any{
		"type":        "permission.request",
		"timestamp":   "2026-07-09T10:11:12Z",
		"decision_id": "perm_42",
		"capability":  "command.exec",
		"resource":    "mv a b",
	}
	th := theme.New(theme.Mono)
	en := renderEvent(ev, th, "en")
	for _, want := range []string{"Approval required", "command.exec", "mv a b", "perm_42"} {
		if !strings.Contains(en, want) {
			t.Errorf("en line missing %q: %q", want, en)
		}
	}
	zh := renderEvent(ev, th, "zh")
	for _, want := range []string{"需要授权", "perm_42"} {
		if !strings.Contains(zh, want) {
			t.Errorf("zh line missing %q: %q", want, zh)
		}
	}
}

// renderEvent must strip ANSI/control sequences out of command stdout
// (payload.chunk) before it enters the transcript: an executed command's
// output is attacker/model-controlled and must not be able to inject
// terminal escape sequences (e.g. to spoof another line, move the cursor,
// or clear the screen) into the TUI.
func TestRenderEventStripsANSIFromCommandOutput(t *testing.T) {
	th := theme.New(theme.Mono)
	malicious := "hello\x1b[31mRED\x1b[0m\x1b]0;pwned\x07 world\x1b[2K\x1b[1;1H"
	line := renderEvent(map[string]any{
		"type":      "CommandOutput",
		"timestamp": "2026-07-09T10:11:12Z",
		"payload":   map[string]any{"chunk": malicious},
	}, th, "en")
	if strings.ContainsAny(line, "\x1b\x07") {
		t.Errorf("rendered line still contains raw escape/control bytes: %q", line)
	}
	for _, want := range []string{"hello", "RED", "world"} {
		if !strings.Contains(line, want) {
			t.Errorf("stripped output lost printable content %q: %q", want, line)
		}
	}
}

// Under Mono (NO_COLOR / --plain) the status glyphs collapse to their ASCII
// fallbacks — no non-ASCII glyph survives in plain output.
func TestRenderEventGlyphASCIIFallbackUnderMono(t *testing.T) {
	th := theme.New(theme.Mono)
	cases := []map[string]any{
		{"type": "permission.request", "timestamp": "2026-07-09T10:11:12Z", "decision_id": "d", "capability": "c", "resource": "r"},
		{"type": "task.completed", "timestamp": "2026-07-09T10:11:12Z", "status": "completed"},
		{"type": "task.completed", "timestamp": "2026-07-09T10:11:12Z", "status": "failed"},
	}
	for _, ev := range cases {
		line := renderEvent(ev, th, "en")
		for _, r := range []string{"✓", "⚿", "✗", "·"} {
			if strings.Contains(line, r) {
				t.Errorf("Mono output contains glyph %q: %q", r, line)
			}
		}
	}
	// Color profiles keep the four-glyph vocabulary.
	color := theme.New(theme.ANSI256)
	line := renderEvent(cases[0], color, "en")
	if !strings.Contains(line, "⚿") {
		t.Errorf("color output missing needs-auth glyph: %q", line)
	}
}
