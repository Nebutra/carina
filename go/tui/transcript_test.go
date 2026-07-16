package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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

func TestAuthoritativeToolLifecycleUpdatesByCallID(t *testing.T) {
	th := theme.New(theme.Mono)
	var tr transcript
	tr.pushPresentation(presentEvent(map[string]any{"type": "ToolCallStarted", "payload": map[string]any{"call_id": "c1", "tool": "run", "status": "running"}}, th, "en"), th, 120)
	tr.pushPresentation(presentEvent(map[string]any{"type": "ToolCallCompleted", "payload": map[string]any{"call_id": "c1", "tool": "run", "status": "completed", "artifact_ids": []any{"sha256:abc"}}}, th, "en"), th, 120)
	if len(tr.entries) != 1 {
		t.Fatalf("entries = %d, want lifecycle merged into one", len(tr.entries))
	}
	tr.toggleLastCollapsible(th, 120)
	got := strings.Join(tr.lines, "\n")
	for _, want := range []string{"completed", "sha256:abc", "carina artifact read"} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline missing %q: %q", want, got)
		}
	}
}

func TestRuntimeStageUsesStableCallKey(t *testing.T) {
	p := presentEvent(map[string]any{"type": "RuntimeStageChanged", "payload": map[string]any{"call_id": "c1", "stage": "executing", "status": "running"}}, theme.New(theme.Mono), "en")
	if p.Key != "stage:c1" || !strings.Contains(p.Summary, "executing") {
		t.Fatalf("presentation = %#v", p)
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

// Prose bodies soft-wrap to the transcript width instead of being
// ellipsis-clipped; every word of the source survives the projection.
func TestPresentationProseBodyWraps(t *testing.T) {
	th := theme.New(theme.Mono)
	summary := strings.TrimSpace(strings.Repeat("the quick 棕色 fox jumps over https://example.com/lazy/dog ", 4))
	p := presentEvent(map[string]any{
		"type": "task.completed", "timestamp": "2026-07-09T10:11:12Z",
		"task_id": "t1", "status": "completed", "summary": summary,
	}, th, "en")
	if !p.BodyProse {
		t.Fatalf("task summary should be a prose body: %#v", p)
	}
	out := p.render(th, 40)
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("long prose did not wrap: %q", out)
	}
	for _, line := range lines[1:] { // header keeps its own fitLine budget
		if strings.Contains(line, "…") {
			t.Errorf("prose body was ellipsis-clipped: %q", line)
		}
	}
	joined := strings.Join(lines, "\n")
	for _, word := range strings.Fields(summary) {
		if !strings.Contains(joined, word) {
			t.Errorf("wrapped body lost %q: %q", word, joined)
		}
	}
}

// Structured bodies — diff hunks and other line-oriented rows — keep the
// existing one-line clipping so their alignment is never reflowed.
func TestPresentationDiffBodyKeepsClipping(t *testing.T) {
	th := theme.New(theme.Mono)
	p := presentEvent(map[string]any{
		"type": "PatchProposed", "timestamp": "2026-07-09T10:11:12Z",
		"payload": map[string]any{"path": "a.go", "diff": "+" + strings.Repeat("x", 100)},
	}, th, "en")
	if p.BodyProse {
		t.Fatalf("diff body must stay structured: %#v", p)
	}
	p.Collapsed = false
	lines := strings.Split(p.render(th, 40), "\n")
	if len(lines) != 2 {
		t.Fatalf("diff line reflowed instead of clipping: %q", lines)
	}
	if !strings.HasSuffix(lines[1], "…") {
		t.Errorf("overflowing diff line not ellipsis-clipped: %q", lines[1])
	}
}

// A resize re-wraps prose from the presentation source: narrower widths give
// more lines, and restoring the original width restores the original lines —
// rendering stays a pure function of (source, width).
func TestResizeRewrapsProseBodyFromSource(t *testing.T) {
	th := theme.New(theme.Mono)
	var tr transcript
	tr.pushPresentation(presentEvent(map[string]any{
		"type": "task.completed", "timestamp": "2026-07-09T10:11:12Z",
		"task_id": "t1", "status": "completed",
		"summary": strings.TrimSpace(strings.Repeat("alpha beta gamma delta ", 8)),
	}, th, "en"), th, 80)
	wide := append([]string(nil), tr.lines...)
	tr.resizePresentations(th, 30)
	if len(tr.lines) <= len(wide) {
		t.Fatalf("narrow resize did not re-wrap: %d -> %d lines", len(wide), len(tr.lines))
	}
	for _, line := range tr.lines[1:] { // the header row keeps its fitLine budget
		if strings.Contains(line, "…") {
			t.Errorf("resized prose was clipped: %q", line)
		}
	}
	tr.resizePresentations(th, 80)
	if strings.Join(tr.lines, "\n") != strings.Join(wide, "\n") {
		t.Errorf("round-trip resize is not deterministic:\n%q\nwant\n%q", tr.lines, wide)
	}
}

// The "done" action's summary is the final assistant response; it renders
// through the markdown pipeline and starts expanded. Every other action keeps
// the plain summary-row presentation.
func TestFinalResponseRendersMarkdown(t *testing.T) {
	th := theme.New(theme.Mono)
	p := presentEvent(map[string]any{
		"type": "ModelResponded", "timestamp": "2026-07-09T10:11:12Z",
		"payload": map[string]any{
			"text": `{"tool":"done","summary":"# 计划 Plan\n\n- first 第一步\n- second\n\n| a | b |\n|---|---|\n| 1 | 2 |"}`,
		},
	}, th, "en")
	if p.BodyMarkdown == "" {
		t.Fatalf("final response did not reach the markdown path: %#v", p)
	}
	if p.Collapsed || !p.Collapsible {
		t.Fatalf("final response must start expanded: %#v", p)
	}
	out := p.render(th, 60)
	for _, want := range []string{"# 计划 Plan", "- first 第一步", "- second", "a | b", "--+--", "1 | 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown body missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("Mono markdown body emitted escapes:\n%q", out)
	}
}

// Non-final actions keep the structured summary rows: markdown is reserved
// for the final-response/plan surface, not intermediate tool selections.
func TestActionSummaryStaysDefaultPresentation(t *testing.T) {
	th := theme.New(theme.Mono)
	p := presentEvent(map[string]any{
		"type": "ModelResponded", "timestamp": "2026-07-09T10:11:12Z",
		"payload": map[string]any{
			"text": `{"tool":"run","summary":"# not markdown","command":["ls"]}`,
		},
	}, th, "en")
	if p.BodyMarkdown != "" {
		t.Fatalf("intermediate action must not use the markdown path: %#v", p)
	}
	if len(p.Body) == 0 || p.Body[0] != "# not markdown" {
		t.Fatalf("action summary lost: %#v", p.Body)
	}
}

// The sanitize boundary is unchanged: escape sequences smuggled through the
// model's JSON never reach the markdown source, while the renderer itself is
// the only origin of styling (OSC 8 hyperlinks included).
func TestMarkdownSourceIsSanitizedAndLinksAreRendererEmitted(t *testing.T) {
	tru := theme.New(theme.TrueColor)
	p := presentEvent(map[string]any{
		"type": "ModelResponded", "timestamp": "2026-07-09T10:11:12Z",
		"payload": map[string]any{
			"text": `{"tool":"done","summary":"safe \u001b[31mred [docs](https://example.com/a)"}`,
		},
	}, tru, "en")
	if strings.Contains(p.BodyMarkdown, "\x1b") {
		t.Fatalf("markdown source kept an escape sequence: %q", p.BodyMarkdown)
	}
	out := p.render(tru, 80)
	if !strings.Contains(out, "\x1b]8;;https://example.com/a\x1b\\") {
		t.Errorf("renderer did not emit the OSC 8 hyperlink:\n%q", out)
	}
	if !strings.Contains(ansi.Strip(out), "safe red") {
		t.Errorf("sanitized prose lost content:\n%q", out)
	}
}

// A resize re-renders the markdown body from source: prose wraps without
// clipping at the narrow width, and restoring the width restores the lines.
func TestResizeReRendersMarkdownFromSource(t *testing.T) {
	th := theme.New(theme.Mono)
	summary := "## Result\n\n" + strings.TrimSpace(strings.Repeat("alpha 词语 beta ", 10))
	var tr transcript
	tr.pushPresentation(presentEvent(map[string]any{
		"type": "ModelResponded", "timestamp": "2026-07-09T10:11:12Z",
		"payload": map[string]any{
			"text": `{"tool":"done","summary":` + strconv.Quote(summary) + `}`,
		},
	}, th, "en"), th, 80)
	wide := append([]string(nil), tr.lines...)
	tr.resizePresentations(th, 28)
	if len(tr.lines) <= len(wide) {
		t.Fatalf("narrow resize did not re-render markdown: %d -> %d lines", len(wide), len(tr.lines))
	}
	for _, line := range tr.lines[1:] { // the header row keeps its fitLine budget
		if strings.Contains(line, "…") {
			t.Errorf("markdown prose was clipped on resize: %q", line)
		}
		if w := ansi.StringWidth(line); w > 28 {
			t.Errorf("markdown prose overflows width 28 (%d): %q", w, line)
		}
	}
	tr.resizePresentations(th, 80)
	if strings.Join(tr.lines, "\n") != strings.Join(wide, "\n") {
		t.Errorf("round-trip resize is not deterministic:\n%q\nwant\n%q", tr.lines, wide)
	}
}
