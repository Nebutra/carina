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
