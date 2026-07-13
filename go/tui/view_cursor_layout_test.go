package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestViewDeclaresPhysicalCursorAtCJKCaretCell(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "zh"})
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.input.SetValue("你a")
	m.input.SetCursorColumn(1) // between the two runes, after a two-cell glyph
	m.layout()

	if m.input.VirtualCursor() {
		t.Fatal("textarea virtual cursor is enabled; terminal IME has no physical caret anchor")
	}
	local := m.input.Cursor()
	if local == nil {
		t.Fatal("focused textarea did not declare a cursor")
	}
	// Prompt "> " is two cells and 你 is two cells. A rune/byte based
	// implementation would incorrectly report 3 or 5 here.
	if local.Position.X != 4 {
		t.Fatalf("textarea CJK cursor column = %d, want 4 display cells", local.Position.X)
	}

	v := m.View()
	if v.Cursor == nil {
		t.Fatal("root view did not publish textarea cursor")
	}
	if got, want := v.Cursor.Position.X, local.Position.X+m.root.inputX; got != want {
		t.Fatalf("root cursor X = %d, want local+frame offset %d", got, want)
	}
	if got, want := v.Cursor.Position.Y, local.Position.Y+m.root.inputY; got != want {
		t.Fatalf("root cursor Y = %d, want local+layout offset %d", got, want)
	}
	if v.Cursor.Position.X >= m.width || v.Cursor.Position.Y >= m.height {
		t.Fatalf("declared cursor outside terminal: %+v in %dx%d", v.Cursor.Position, m.width, m.height)
	}
}

func TestOverlayArbitratesAndRestoresPhysicalCursor(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 36, Height: 10})
	m.input.SetValue("draft")
	m.layout()
	before := m.View().Cursor
	if before == nil {
		t.Fatal("precondition: composer cursor is not visible")
	}

	m.approval = &approvalState{
		DecisionID: "perm_1",
		Action:     "command.exec",
		Resource:   "go test ./...",
	}
	if cursor := m.View().Cursor; cursor != nil {
		t.Fatalf("approval overlay leaked the composer cursor: %+v", cursor.Position)
	}

	m.approval = nil
	after := m.View().Cursor
	if after == nil {
		t.Fatal("composer cursor was not restored after overlay closed")
	}
	if after.Position != before.Position {
		t.Fatalf("restored cursor = %+v, want freshly computed %+v", after.Position, before.Position)
	}
}

func TestViewFitsConstrainedTerminalAndKeepsCursorInBounds(t *testing.T) {
	for _, width := range []int{1, 2, 3, 4, 5, 6, 12} {
		for _, height := range []int{1, 2, 3, 4, 6, 7, 10} {
			t.Run(testSizeName(width, height), func(t *testing.T) {
				m := New(Options{Theme: theme.New(theme.Mono), Locale: "zh"})
				m.input.SetValue("x你y")
				m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				m.push("输出 output")
				m.layout()

				v := m.View()
				assertViewFits(t, v.Content, width, height)
				if v.Cursor == nil {
					t.Fatal("usable terminal lost the active composer cursor")
				}
				if v.Cursor.Position.X < 0 || v.Cursor.Position.X >= width ||
					v.Cursor.Position.Y < 0 || v.Cursor.Position.Y >= height {
					t.Fatalf("cursor %+v outside %dx%d", v.Cursor.Position, width, height)
				}
			})
		}
	}
}

func TestOversizedModalFitsTightTerminalAndHidesCursor(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 16, Height: 4})
	m.approval = &approvalState{
		DecisionID: "perm_long",
		Action:     "command.exec",
		Resource:   strings.Repeat("dangerous command ", 8),
		Reason:     strings.Repeat("policy explanation ", 8),
		Body:       []string{strings.Repeat("+diff ", 30)},
	}

	v := m.View()
	assertViewFits(t, v.Content, 16, 4)
	if v.Cursor != nil {
		t.Fatalf("modal declared a background input cursor: %+v", v.Cursor.Position)
	}
}

func TestFrameReservesBorderCellsWithoutLosingRightEdge(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 12, Height: 8})
	view := m.View().Content
	for _, line := range strings.Split(view, "\n") {
		if !strings.HasPrefix(line, "╭") {
			continue
		}
		if !strings.HasSuffix(line, "╮") {
			t.Fatalf("frame right border was clipped: %q", line)
		}
		if got := ansi.StringWidth(line); got != 12 {
			t.Fatalf("frame width = %d, want terminal width 12: %q", got, line)
		}
		return
	}
	t.Fatalf("no complete top frame border found:\n%s", view)
}

func TestFitViewBlockPreservesRenderedANSI(t *testing.T) {
	colored := "\x1b[31mabcdef\x1b[0m"
	got := fitViewBlock(colored, 4, 1, false)
	if !strings.Contains(got, "\x1b[31m") {
		t.Fatalf("render clipping stripped ANSI style: %q", got)
	}
	if width := ansi.StringWidth(got); width != 4 {
		t.Fatalf("clipped rendered width = %d, want 4: %q", width, got)
	}
}

func TestMonoTextareaDoesNotLeakDefaultANSI(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 28, Height: 8})
	if got := m.View().Content; strings.Contains(got, "\x1b[") {
		t.Fatalf("Mono textarea leaked ANSI styling: %q", got)
	}
}

func assertViewFits(t *testing.T, content string, width, height int) {
	t.Helper()
	if content == "" {
		return
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		t.Fatalf("view height = %d, exceeds terminal height %d:\n%s", len(lines), height, content)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line %d width = %d, exceeds terminal width %d: %q", i, got, width, line)
		}
	}
}

func testSizeName(width, height int) string {
	return fmt.Sprintf("%dx%d", width, height)
}
