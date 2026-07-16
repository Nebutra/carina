package markdown

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

// Fenced code with a known language highlights through the syntax theme
// roles: keyword Dust Violet, string Spectral Green, number Copper Amber,
// comment italic. The plain text stays byte-identical under ansi.Strip.
func TestHighlightStylesThroughThemeRoles(t *testing.T) {
	src := "```go\n// note\nreturn \"s\" + 42\n```"
	out := Render(src, theme.New(theme.TrueColor), 80, "", passWrap)
	joined := strings.Join(out, "\n")
	for _, want := range []struct{ name, seq string }{
		{"keyword Dust Violet", "38;2;198;166;234"},   // #c6a6ea
		{"string Spectral Green", "38;2;104;210;163"}, // #68d2a3
		{"number Copper Amber", "38;2;232;168;95"},    // #e8a85f
		{"comment Dust", "38;2;176;183;179"},          // #b0b7b3
	} {
		if !strings.Contains(joined, want.seq) {
			t.Errorf("highlighted block missing %s (%q): %q", want.name, want.seq, joined)
		}
	}
	if !strings.Contains(joined, "\x1b[3;") && !strings.Contains(joined, "\x1b[3m") {
		t.Errorf("comment should carry the italic attribute: %q", joined)
	}
	plain := make([]string, len(out))
	for i, line := range out {
		plain[i] = ansi.Strip(line)
	}
	if got, want := strings.Join(plain, "\n"), "// note\nreturn \"s\" + 42"; got != want {
		t.Errorf("highlighting altered the text: %q, want %q", got, want)
	}
}

// Mono renders highlighted-language fences exactly like plain ones: verbatim,
// escape-free (the NO_COLOR contract skips tokenization entirely).
func TestHighlightMonoStaysPlain(t *testing.T) {
	out := Render("```go\nreturn 1\n```", theme.New(theme.Mono), 80, "", passWrap)
	if len(out) != 1 || out[0] != "return 1" {
		t.Errorf("Mono fenced code = %#v", out)
	}
}

// Guardrails: an unknown language, an absent language, or a block past the
// line budget falls back to plain RoleCodeBlock (Dust) with no syntax colors.
func TestHighlightGuardrails(t *testing.T) {
	tru := theme.New(theme.TrueColor)
	keyword := "38;2;198;166;234" // Dust Violet would only appear via highlighting
	dust := "38;5;249"

	for _, src := range []string{
		"```nosuchlanguage\nreturn 1\n```",
		"```\nreturn 1\n```",
	} {
		out := strings.Join(Render(src, tru, 80, "", passWrap), "\n")
		if strings.Contains(out, keyword) {
			t.Errorf("unhighlightable fence must not carry syntax colors: %q", out)
		}
	}

	huge := "```go\n" + strings.Repeat("return 1\n", highlightMaxLines+1) + "```"
	out := Render(huge, theme.New(theme.ANSI256), 80, "", passWrap)
	if len(out) != highlightMaxLines+1 {
		t.Fatalf("guardrail render lost lines: %d", len(out))
	}
	first := out[0]
	if !strings.Contains(first, dust) || strings.Contains(strings.Join(out[:10], "\n"), "38;5;182") {
		t.Errorf("oversized block must render plain RoleCodeBlock: %q", first)
	}

	if highlightEligible(strings.Repeat("x", highlightMaxBytes+1)) {
		t.Error("blocks above the byte budget must not be eligible")
	}
	if !highlightEligible("return 1\n") {
		t.Error("a small block must be eligible")
	}
}

// Same source, same theme, same lines — chroma stays inside the pure-render
// contract.
func TestHighlightIsDeterministic(t *testing.T) {
	src := "```python\ndef f(x):\n    return x + 1  # inc\n```"
	th := theme.New(theme.TrueColor)
	first := strings.Join(Render(src, th, 80, "", passWrap), "\n")
	for i := 0; i < 3; i++ {
		if got := strings.Join(Render(src, th, 80, "", passWrap), "\n"); got != first {
			t.Fatalf("highlight render %d diverged", i)
		}
	}
}
