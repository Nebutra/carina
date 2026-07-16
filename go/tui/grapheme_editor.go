package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"
)

// handleGraphemeEditorKey closes the gap between Carina's semantic keymap and
// Bubbles textarea's rune-based character editing. Textarea remains the owner
// of text storage, rendering, word movement, and vertical scroll state; Carina
// only intercepts operations that must treat a Unicode grapheme as one atom.
func (m *Model) handleGraphemeEditorKey(key string) (tea.Cmd, bool) {
	action := KeyAction("")
	// Vertical motion stays with the textarea (history vs caret boundary is
	// decided earlier). Intercepting up/down here would mark the key handled
	// and break multiline caret navigation tests and bubbles' line movement.
	for _, candidate := range []KeyAction{
		ActionEditorMoveLeft,
		ActionEditorMoveRight,
		ActionEditorDeleteBackward,
		ActionEditorDeleteForward,
		ActionEditorKillLineStart,
		ActionEditorKillLineEnd,
		ActionEditorTransposeBackward,
	} {
		if m.keys.matches(KeyContextEditor, candidate, key) {
			action = candidate
			break
		}
	}
	if action == "" {
		return nil, false
	}

	before := m.composerSnapshot()
	lines := splitComposerLines(before.draft.Text)
	row := clampInt(before.row, 0, len(lines)-1)
	line := lines[row]
	runes := []rune(line)
	col := clampInt(before.col, 0, len(runes))
	boundaries := graphemeRuneBoundaries(line)

	switch action {
	case ActionEditorMoveLeft:
		if col > 0 {
			m.setComposerCaret(row, previousGraphemeBoundary(boundaries, col))
		} else if row > 0 {
			m.setComposerCaret(row-1, len([]rune(lines[row-1])))
		}
	case ActionEditorMoveRight:
		if col < len(runes) {
			m.setComposerCaret(row, nextGraphemeBoundary(boundaries, col))
		} else if row+1 < len(lines) {
			m.setComposerCaret(row+1, 0)
		}
	case ActionEditorDeleteBackward:
		if start, end, ok := graphemeRangeAtCaret(boundaries, col, true); ok {
			lines[row] = string(append(append([]rune(nil), runes[:start]...), runes[end:]...))
			m.replaceComposerLines(before, lines, row, start)
		} else if col == 0 && row > 0 {
			previousLen := len([]rune(lines[row-1]))
			lines[row-1] += lines[row]
			lines = append(lines[:row], lines[row+1:]...)
			m.replaceComposerLines(before, lines, row-1, previousLen)
		}
	case ActionEditorDeleteForward:
		if start, end, ok := graphemeRangeAtCaret(boundaries, col, false); ok {
			lines[row] = string(append(append([]rune(nil), runes[:start]...), runes[end:]...))
			m.replaceComposerLines(before, lines, row, start)
		} else if col == len(runes) && row+1 < len(lines) {
			lines[row] += lines[row+1]
			lines = append(lines[:row+1], lines[row+2:]...)
			m.replaceComposerLines(before, lines, row, col)
		} else {
			// Nothing to delete: fall through so empty Ctrl+D can exit (GlobalExit)
			// and non-empty Ctrl+D can reach bubbles/default delete-forward handling.
			return nil, false
		}
	case ActionEditorKillLineStart:
		cut := col
		if start, end, ok := graphemeRangeAtCaret(boundaries, col, false); ok && start < col {
			cut = end
		}
		lines[row] = string(runes[cut:])
		m.replaceComposerLines(before, lines, row, 0)
	case ActionEditorKillLineEnd:
		cut := col
		if start, _, ok := graphemeRangeAtCaret(boundaries, col, true); ok && start < col && !isGraphemeBoundary(boundaries, col) {
			cut = start
		}
		lines[row] = string(runes[:cut])
		m.replaceComposerLines(before, lines, row, cut)
	case ActionEditorTransposeBackward:
		if next, nextCol, ok := transposeGraphemesAtCaret(line, col); ok {
			lines[row] = next
			m.replaceComposerLines(before, lines, row, nextCol)
		}
	}

	m.layout()
	m.recordComposerEdit(before, composerEditOther)
	return m.refreshSuggestTrigger(), true
}

func (m *Model) replaceComposerLines(before composerSnapshot, lines []string, row, col int) {
	after := before
	after.draft.Text = strings.Join(lines, "\n")
	after.row = row
	after.col = col
	m.restoreComposerSnapshot(after)
}

func (m *Model) setComposerCaret(row, col int) {
	if m.input.Line() == row {
		m.input.SetCursorColumn(col)
		return
	}
	m.input.MoveToBegin()
	guard := len([]rune(m.input.Value())) + m.input.LineCount() + 1
	for steps := 0; m.input.Line() < row && steps < guard; steps++ {
		m.input.CursorDown()
	}
	m.input.SetCursorColumn(col)
}

func (m *Model) snapComposerCaretToGraphemeBoundary() {
	line := currentLine(m.input.Value(), m.input.Line())
	boundaries := graphemeRuneBoundaries(line)
	col := m.input.Column()
	if isGraphemeBoundary(boundaries, col) {
		return
	}
	m.input.SetCursorColumn(previousGraphemeBoundary(boundaries, col))
}

// snapVerticalGraphemeEditorKey lets textarea retain its visual-row movement
// and preferred-column state, then repairs the rare landing point that falls
// inside an extended grapheme on the destination row.
func (m *Model) snapVerticalGraphemeEditorKey(key string) {
	if !m.keys.matches(KeyContextEditor, ActionEditorMoveUp, key) &&
		!m.keys.matches(KeyContextEditor, ActionEditorMoveDown, key) {
		return
	}
	m.snapComposerCaretToGraphemeBoundary()
}

func graphemeRuneBoundaries(s string) []int {
	boundaries := []int{0}
	graphemes := uniseg.NewGraphemes(s)
	pos := 0
	for graphemes.Next() {
		pos += len([]rune(graphemes.Str()))
		boundaries = append(boundaries, pos)
	}
	return boundaries
}

func previousGraphemeBoundary(boundaries []int, col int) int {
	previous := 0
	for _, boundary := range boundaries {
		if boundary >= col {
			break
		}
		previous = boundary
	}
	return previous
}

func nextGraphemeBoundary(boundaries []int, col int) int {
	for _, boundary := range boundaries {
		if boundary > col {
			return boundary
		}
	}
	return boundaries[len(boundaries)-1]
}

func isGraphemeBoundary(boundaries []int, col int) bool {
	for _, boundary := range boundaries {
		if boundary == col {
			return true
		}
	}
	return false
}

func graphemeRangeAtCaret(boundaries []int, col int, backward bool) (int, int, bool) {
	if len(boundaries) < 2 {
		return 0, 0, false
	}
	end := boundaries[len(boundaries)-1]
	col = clampInt(col, 0, end)
	for i := 0; i+1 < len(boundaries); i++ {
		start, next := boundaries[i], boundaries[i+1]
		switch {
		case col > start && col < next:
			return start, next, true
		case backward && col == next:
			return start, next, true
		case !backward && col == start:
			return start, next, true
		}
	}
	return 0, 0, false
}

func dropLastGrapheme(s string) string {
	boundaries := graphemeRuneBoundaries(s)
	if len(boundaries) < 2 {
		return ""
	}
	runes := []rune(s)
	return string(runes[:boundaries[len(boundaries)-2]])
}

func transposeGraphemesAtCaret(s string, col int) (string, int, bool) {
	boundaries := graphemeRuneBoundaries(s)
	clusterCount := len(boundaries) - 1
	if clusterCount < 2 || col <= 0 {
		return s, col, false
	}
	col = clampInt(col, 0, boundaries[len(boundaries)-1])
	right := clusterCount - 1
	if col < boundaries[len(boundaries)-1] {
		for i := 0; i < clusterCount; i++ {
			if col <= boundaries[i] {
				right = i
				break
			}
			if col > boundaries[i] && col < boundaries[i+1] {
				right = i
				break
			}
		}
	}
	left := right - 1
	if left < 0 {
		return s, col, false
	}
	runes := []rune(s)
	leftStart, middle, rightEnd := boundaries[left], boundaries[right], boundaries[right+1]
	out := make([]rune, 0, len(runes))
	out = append(out, runes[:leftStart]...)
	out = append(out, runes[middle:rightEnd]...)
	out = append(out, runes[leftStart:middle]...)
	out = append(out, runes[rightEnd:]...)
	return string(out), rightEnd, true
}

func splitComposerLines(value string) []string {
	// strings.Split preserves a trailing empty logical row, matching textarea.
	return strings.Split(value, "\n")
}
