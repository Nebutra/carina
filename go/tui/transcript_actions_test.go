package tui

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestTranscriptCopyUsesRenderedAgentProjection(t *testing.T) {
	th := theme.New(theme.ANSI256)
	tr := transcript{}
	tr.push("operator-only text")
	tr.pushPresentation(eventPresentation{
		Kind: presentationAgent, Status: statusSuccess, Title: "answer",
		Summary: "done", Body: []string{"visible result"},
	}, th, 80)

	got := tr.lastAgentText()
	if strings.Contains(got, "\x1b[") || !strings.Contains(got, "visible result") {
		t.Fatalf("copied agent text = %q", got)
	}
	if strings.Contains(got, "operator-only") {
		t.Fatalf("copy leaked unrelated transcript entry: %q", got)
	}
}

func TestTranscriptPlainTextStripsTerminalStyling(t *testing.T) {
	tr := transcript{}
	tr.push("\x1b[31mred\x1b[0m")
	if got := tr.plainText(); got != "red" {
		t.Fatalf("plain transcript = %q", got)
	}
}
