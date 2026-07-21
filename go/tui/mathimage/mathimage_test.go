package mathimage

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderQueuesProtocolOutsidePlaceholderLines(t *testing.T) {
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	lines, ok := Render(`\frac{carina_7}{\sqrt{x}}`, 80, "  ")
	if !ok || len(lines) < 2 {
		t.Fatalf("pixel formula did not render: ok=%v rows=%d", ok, len(lines))
	}
	for _, line := range lines {
		if strings.Contains(line, "\x1b_G") {
			t.Fatal("graphics APC leaked into cell-buffer content")
		}
		if ansi.StringWidth(line) <= 2 || ansi.StringWidth(line) > 82 {
			t.Fatalf("placeholder width is outside its cell budget: %d", ansi.StringWidth(line))
		}
	}
	raw := Drain()
	for _, want := range []string{"\x1b_Ga=t,f=100", "\x1b_Ga=p,U=1", "\x1b\\"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("protocol output missing %q", want)
		}
	}
	if again := Drain(); again != "" {
		t.Fatalf("transmit was not one-shot: %d bytes", len(again))
	}
}

func TestUnsupportedTerminalFailsClosed(t *testing.T) {
	t.Setenv("CARINA_MATH_GRAPHICS", "off")
	if lines, ok := Render(`x^2`, 80, ""); ok || lines != nil {
		t.Fatalf("disabled graphics rendered: ok=%v lines=%q", ok, lines)
	}
}

func TestOversizedTexFailsClosedBeforeParsing(t *testing.T) {
	t.Setenv("CARINA_MATH_GRAPHICS", "kitty")
	if lines, ok := Render(strings.Repeat("x", maxTexBytes+1), 80, ""); ok || lines != nil {
		t.Fatalf("oversized formula rendered: ok=%v lines=%d", ok, len(lines))
	}
}
