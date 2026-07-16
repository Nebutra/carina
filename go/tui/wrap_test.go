package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/markdown"
	"github.com/Nebutra/carina/go/tui/theme"
)

func TestWrapText(t *testing.T) {
	cases := []struct {
		name                string
		in                  string
		width               int
		initial, subsequent string
		want                []string
	}{
		{
			name: "fits unchanged", in: "hello world", width: 20,
			want: []string{"hello world"},
		},
		{
			name: "simple word wrap", in: "alpha beta gamma", width: 10,
			want: []string{"alpha beta", "gamma"},
		},
		{
			name: "initial and subsequent indent", in: "alpha beta gamma", width: 12,
			initial: "* ", subsequent: "  ",
			want: []string{"* alpha beta", "  gamma"},
		},
		{
			name: "hanging indent narrower than first", in: "one two three four", width: 9,
			initial: "  - ", subsequent: "    ",
			want: []string{"  - one", "    two", "    three", "    four"},
		},
		{
			name: "cjk breaks at grapheme boundary", in: "宇宙飞船正在降落", width: 7,
			want: []string{"宇宙飞", "船正在", "降落"},
		},
		{
			name: "cjk never splits a wide cell", in: "日本語のテキスト", width: 5,
			want: []string{"日本", "語の", "テキ", "スト"},
		},
		{
			name: "mixed width words", in: "log: 结果成功 done", width: 10,
			want: []string{"log:", "结果成功", "done"},
		},
		{
			name: "cjk with indent", in: "任务已经完成", width: 8,
			initial: "  ", subsequent: "  ",
			want: []string{"  任务已", "  经完成"},
		},
		{
			name: "long ascii token hard-broken", in: "abcdefghijkl", width: 5,
			want: []string{"abcde", "fghij", "kl"},
		},
		{
			name: "url never broken mid-token",
			in:   "see https://example.com/some/very/long/path?query=1 now", width: 20,
			want: []string{"see", "https://example.com/some/very/long/path?query=1", "now"},
		},
		{
			name: "url that fits moves whole to next line",
			in:   "docs at https://a.io/x", width: 16,
			want: []string{"docs at", "https://a.io/x"},
		},
		{
			name: "www token never broken",
			in:   "visit www.example.com/aaaaaaaaaaaa today", width: 10,
			want: []string{"visit", "www.example.com/aaaaaaaaaaaa", "today"},
		},
		{
			name: "bracketed url still recognized",
			in:   "(https://example.com/very/long/component/path)", width: 12,
			want: []string{"(https://example.com/very/long/component/path)"},
		},
		{
			name: "authored leading spaces survive first line",
			in:   "  indented text", width: 30,
			want: []string{"  indented text"},
		},
		{
			name: "internal spacing kept when it fits", in: "a  b", width: 10,
			want: []string{"a  b"},
		},
		{
			name: "blank line keeps indent", in: "", width: 10,
			initial: "  ", subsequent: "  ",
			want: []string{"  "},
		},
		{
			name: "zero width degrades like fitLine", in: "hi", width: 0,
			want: []string{""},
		},
		{
			name: "wide grapheme in one-cell budget still advances", in: "宽字", width: 1,
			want: []string{"宽", "字"},
		},
		{
			name: "emoji sequences stay atomic", in: "👩🏽‍💻🇨🇳1️⃣", width: 2,
			want: []string{"👩🏽‍💻", "🇨🇳", "1️⃣"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapText(tc.in, tc.width, tc.initial, tc.subsequent)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("wrapText(%q, %d) = %#v, want %#v", tc.in, tc.width, got, tc.want)
			}
		})
	}
}

// Wrapping must never invent an ellipsis or drop content: every input word
// survives, and lines fit the width unless a URL token forced an overflow.
func TestWrapTextIsLossless(t *testing.T) {
	in := strings.Repeat("word 词语 ", 30) + "https://example.com/path"
	got := wrapText(in, 24, "", "  ")
	joined := strings.Join(got, "\n")
	for _, word := range strings.Fields(in) {
		if !strings.Contains(joined, word) {
			t.Errorf("wrapped output lost %q", word)
		}
	}
	if strings.Contains(joined, "…") {
		t.Errorf("wrapped output contains an ellipsis: %q", joined)
	}
	for _, line := range got {
		if w := ansi.StringWidth(line); w > 24 && !strings.Contains(line, "://") {
			t.Errorf("non-URL line exceeds width: %d %q", w, line)
		}
	}
}

// An OSC 8 hyperlink whose label wraps must close at every soft break and
// re-open after the continuation indent: transcript lines are windowed
// independently by the viewport, so a hyperlink left open at a line end would
// claim every cell painted after it (later transcript lines, composer,
// status chrome) with a model-supplied destination whenever the closing line
// scrolls out of view.
func TestWrapTextClosesHyperlinkAtSoftBreaks(t *testing.T) {
	openSeq := "\x1b]8;;https://evil.example/x\x1b\\"
	in := openSeq + "\x1b[4mclick here for the amazing offer right now\x1b[0m" + osc8Close
	got := wrapText(in, 20, "", "  ")
	if len(got) < 2 {
		t.Fatalf("expected the label to wrap, got %#v", got)
	}
	for i, line := range got {
		opens := strings.Count(line, "\x1b]8;;") - strings.Count(line, osc8Close)
		closes := strings.Count(line, osc8Close)
		if opens != closes {
			t.Errorf("line %d has %d hyperlink opens but %d closes: %q", i, opens, closes, line)
		}
		if closes < 1 {
			t.Errorf("line %d carries no hyperlink at all — the label lost its destination: %q", i, line)
		}
		if !strings.Contains(line, "https://evil.example/x") {
			t.Errorf("line %d does not re-open the original destination: %q", i, line)
		}
		if !strings.HasSuffix(line, osc8Close) {
			t.Errorf("line %d must end with the hyperlink closed: %q", i, line)
		}
	}
	joined := ansi.Strip(strings.Join(got, " "))
	for _, word := range strings.Fields("click here for the amazing offer right now") {
		if !strings.Contains(joined, word) {
			t.Errorf("label word %q lost across the wrap: %q", word, joined)
		}
	}
}

// The same guarantee end to end: markdown link rendering through the real
// wrapText (the transcript's wiring) never leaves a hyperlink open across a
// stored viewport line, CJK context included.
func TestMarkdownLinkWrapKeepsHyperlinkBalancedPerLine(t *testing.T) {
	th := theme.New(theme.TrueColor)
	lines := markdown.Render("看看 [click here for the details](https://example.com/x) 结尾", th, 14, "", wrapText)
	if len(lines) < 2 {
		t.Fatalf("expected wrapped output, got %#v", lines)
	}
	for i, line := range lines {
		total := strings.Count(line, "\x1b]8;")
		closes := strings.Count(line, osc8Close)
		if opens := total - closes; opens != closes {
			t.Errorf("line %d hyperlink state unbalanced (%d opens, %d closes): %q", i, opens, closes, line)
		}
	}
}

// A theme span that crosses a soft break is closed at the line end and
// re-opened after the continuation indent, so styling never bleeds into the
// indent or a neighbouring viewport line.
func TestWrapTextCarriesStyleAcrossBreaks(t *testing.T) {
	styled := "\x1b[36malpha beta gamma\x1b[0m"
	got := wrapText(styled, 10, "", "> ")
	want := []string{"\x1b[36malpha beta\x1b[0m", "> \x1b[36mgamma\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapText = %#v, want %#v", got, want)
	}
	// Mono emits no sequences, so the same call under a Mono-styled source is
	// plain passthrough.
	mono := theme.New(theme.Mono).Style(theme.RoleTitle).Render("alpha beta gamma")
	if plain := wrapText(mono, 10, "", "> "); !reflect.DeepEqual(plain, []string{"alpha beta", "> gamma"}) {
		t.Errorf("Mono wrap = %#v", plain)
	}
}
