package tui

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestHelpFitsNarrowTerminalAndIncludesCJKSearch(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "zh"})
	m.width, m.height = 28, 12
	m.push("结果 你好世界")
	m.showHelp()
	if v := m.View().Content; !strings.Contains(v, "Carina 帮助") {
		t.Fatalf("help overlay is not immediately visible:\n%s", v)
	}
	m.closeHelp()
	m.slashCommand("/search 你好")
	v := m.View().Content
	if !strings.Contains(v, "记录搜索") {
		t.Fatalf("search result missing:\n%s", v)
	}
	for _, line := range strings.Split(v, "\n") {
		if len([]rune(line)) > 80 {
			t.Fatalf("unbounded narrow-terminal line: %q", line)
		}
	}
}
