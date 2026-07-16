package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func buildFenceTranscript(lang string) *transcript {
	var code strings.Builder
	for i := 0; i < 40; i++ {
		code.WriteString(fmt.Sprintf("func f%d(x int) int { return x * %d } // comment\n", i, i))
	}
	md := "Here is the result:\n\n```" + lang + "\n" + code.String() + "```\n"
	tr := &transcript{}
	th := theme.New(theme.TrueColor)
	for i := 0; i < 10; i++ {
		tr.pushPresentation(eventPresentation{
			Key:          fmt.Sprintf("msg-%d", i),
			Kind:         presentationAgent,
			KindLabel:    "agent",
			Title:        "response",
			BodyMarkdown: md,
		}, th, 100)
	}
	return tr
}

func BenchmarkResizeHighlighted(b *testing.B) {
	tr := buildFenceTranscript("go")
	th := theme.New(theme.TrueColor)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.resizePresentations(th, 100) // same width every time, like layout() per keystroke
	}
}

func BenchmarkResizePlainFence(b *testing.B) {
	tr := buildFenceTranscript("")
	th := theme.New(theme.TrueColor)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.resizePresentations(th, 100)
	}
}
