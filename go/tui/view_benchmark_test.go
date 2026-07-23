package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

var benchmarkRenderedView string

// BenchmarkProductionViewRender exercises the same Model.View path used by the
// shipped TUI with a full workspace-sized transcript and an active composer.
func BenchmarkProductionViewRender(b *testing.B) {
	m := New(Options{Theme: theme.New(theme.ANSI256), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	for i := 0; i < 80; i++ {
		m.push(fmt.Sprintf("tool output %03d: %s", i, strings.Repeat("rendered terminal content ", 3)))
	}
	m.input.SetValue("Inspect the workspace and summarize the remaining production gaps")
	m.layout()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkRenderedView = m.View().Content
	}
}

func BenchmarkTranscriptViewportRender(b *testing.B) {
	for _, size := range []int{2000, 10000} {
		b.Run(fmt.Sprintf("lines_%d", size), func(b *testing.B) {
			m := New(Options{Theme: theme.New(theme.ANSI256), Locale: "en"})
			defer m.Close()
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
			m.tr.lines = make([]string, size)
			for i := range m.tr.lines {
				m.tr.lines[i] = fmt.Sprintf("tool output %05d: rendered terminal content", i)
			}
			m.layout()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkRenderedView = m.View().Content
			}
		})
	}
}

// benchmarkGoFence builds a fenced Go block of n lines — the chroma-eligible
// shape the plain-string benchmark above structurally cannot exercise.
func benchmarkGoFence(n int) string {
	var b strings.Builder
	b.WriteString("```go\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "func f%d() int { return %d } // 说明\n", i, i)
	}
	b.WriteString("```\n")
	return b.String()
}

// BenchmarkLayoutWithHighlightedMarkdown guards the per-keystroke layout()
// path with highlighted markdown bodies present: resizePresentations must be
// a no-op while width and theme are unchanged, or every keystroke re-runs
// goldmark and chroma over the whole session history.
func BenchmarkLayoutWithHighlightedMarkdown(b *testing.B) {
	m := New(Options{Theme: theme.New(theme.ANSI256), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	for i := 0; i < 10; i++ {
		m.tr.pushPresentation(eventPresentation{
			Key:          fmt.Sprintf("md:bench:%d", i),
			Headerless:   true,
			BodyMarkdown: benchmarkGoFence(40),
		}, m.th, m.transcriptWidth())
	}
	m.layout()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.layout()
	}
}

// BenchmarkStreamingMidLineDelta measures the per-token hot path while a
// highlighted fence is held back in the mutable tail: a delta that completes
// no line leaves the newline-gated tail byte-identical and must stay cheap.
func BenchmarkStreamingMidLineDelta(b *testing.B) {
	m := New(Options{Theme: theme.New(theme.ANSI256), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	for i := 0; i < 40; i++ {
		m.push(fmt.Sprintf("tool output %03d", i))
	}
	m.applyStreamDelta("bench", "2026-07-09T10:11:12Z", strings.TrimSuffix(benchmarkGoFence(60), "```\n"), false)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.applyStreamDelta("bench", "2026-07-09T10:11:12Z", "x", false)
	}
}

// BenchmarkStreamingLineDelta is the completing-line variant: the tail really
// changes and re-renders, which is the honest per-line streaming cost the
// perf gate should watch.
func BenchmarkStreamingLineDelta(b *testing.B) {
	m := New(Options{Theme: theme.New(theme.ANSI256), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	for i := 0; i < 40; i++ {
		m.push(fmt.Sprintf("tool output %03d", i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.applyStreamDelta("bench", "2026-07-09T10:11:12Z", fmt.Sprintf("streamed prose line %d\n\n", i), false)
	}
}
