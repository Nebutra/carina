package tui

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

// rootLayout is the single source of truth for both rendering and physical
// cursor placement. Keeping these values together prevents a visually correct
// textarea from publishing a stale IME anchor after a resize or panel change.
type rootLayout struct {
	width, height  int
	framed         bool
	showBanner     bool
	taskLines      int
	queueLines     int
	pasteLines     int
	suggestLines   int
	historyLines   int
	showTranscript bool
	showStatus     bool
	viewportHeight int
	inputHeight    int
	inputX, inputY int
	historyY       int
}

// layout recomputes component sizes. The prompt grows with its content but is
// capped so the transcript always keeps the majority of the terminal.
func (m *Model) layout() {
	m.configureInput()
	l := m.calculateLayout()
	m.root = l
	// The transcript is an unframed document surface with one-cell side
	// breathing room. The composer owns the only persistent border.
	contentWidth := maxInt(l.width-2, 1)
	m.vp.SetWidth(contentWidth)
	m.vp.SetHeight(maxInt(l.viewportHeight, 1))
	m.tr.resizePresentations(m.th, m.transcriptWidth())
	m.vp.SetContentLines(m.tr.lines)
	if m.followTail {
		m.vp.GotoBottom()
	}
}

func (m *Model) configureInput() {
	w, h := m.width, m.height
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	framed := w >= 6 && h >= 5
	// At two rows an active history search uses the non-input row for its
	// query/status instead of the generic status bar.
	showStatus := h >= 2 && (m.historySearch == nil || h >= 3)
	m.applyComposerChrome()
	// applyComposerChrome sets Prompt/Placeholder; width/height follow.
	inputWidth := w
	if framed {
		inputWidth = maxInt(w-2, 1)
	}

	maxInputHeight := h / 3
	if maxInputHeight < 1 {
		maxInputHeight = 1
	}
	if maxInputHeight > 10 {
		maxInputHeight = 10
	}
	reserved := 0
	if showStatus {
		reserved++
	}
	if framed {
		reserved += 3 // composer borders plus one transcript row
	} else if h-reserved > 1 {
		reserved++ // one transcript row
	}
	if available := h - reserved; available < maxInputHeight {
		maxInputHeight = maxInt(available, 1)
	}
	m.input.MaxHeight = maxInputHeight
	m.input.SetWidth(inputWidth)
}

// calculateLayout allocates rows in interaction order. The input is always
// retained; status and one transcript row follow when space permits. Decorative
// borders appear only when both can fit without pushing the footer below the
// terminal. Suggest/task/banner rows consume remaining headroom and are
// truncated before the transcript is allowed to disappear.
func (m *Model) calculateLayout() rootLayout {
	w, h := m.width, m.height
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	l := rootLayout{width: w, height: h, showStatus: h >= 2 && (m.historySearch == nil || h >= 3)}
	l.framed = w >= 6 && h >= 5
	l.inputHeight = m.input.Height()

	borderRows := 0
	if l.framed {
		borderRows = 2
		l.inputX = 1
	}
	remaining := h - l.inputHeight - borderRows
	if l.showStatus {
		remaining--
	}
	if m.historySearch != nil && remaining > 0 {
		l.historyLines = 1
		remaining--
	}
	if remaining > 0 {
		l.showTranscript = true
		l.viewportHeight = 1
		remaining--
	}

	// Queued follow-ups, paste previews and suggestions are part of the active typing flow, then
	// task context, then connection state. Each is bounded by the rows left
	// after core controls.
	l.suggestLines = minInt(len(m.suggestPanelLines()), remaining)
	remaining -= l.suggestLines
	l.queueLines = minInt(len(m.queuePanelLines()), remaining)
	remaining -= l.queueLines
	l.pasteLines = minInt(len(m.pastePanelLines()), remaining)
	remaining -= l.pasteLines
	// Compact mode (Grok /compact-mode) keeps typing chrome and drops task-tree
	// and decorative spacer so the transcript stays dominant.
	if !m.compactMode {
		l.taskLines = minInt(len(m.taskTreeLines()), remaining)
		remaining -= l.taskLines
	}
	if m.banner() != "" && remaining > 0 {
		l.showBanner = true
		remaining--
	}
	if l.showTranscript {
		l.viewportHeight += remaining
	}

	y := 0
	if l.showBanner {
		y++
	}
	y += l.taskLines
	if l.showTranscript {
		y += l.viewportHeight
	}
	y += l.suggestLines
	y += l.queueLines
	y += l.pasteLines
	l.historyY = y
	y += l.historyLines
	if l.framed {
		y++ // input frame's top border
	}
	l.inputY = y
	return l
}

// suggestPanelLines renders the mention/slash suggestion panel as plain
// lines (not a full-frame overlay — the operator is still mid-typing, so
// unlike the approval/question overlays this must not take over the
// screen). Empty when no panel is open, which is what makes it safe to use
// both for layout height reservation and for View()'s own render.
func (m *Model) suggestPanelLines() []string {
	if m.suggest == nil || len(m.suggest.Matches) == 0 {
		return nil
	}
	title := m.text(MsgSuggestFiles, nil)
	if m.suggest.Kind == mentionCommand {
		title = m.text(MsgSuggestCommands, nil)
	}
	lines := make([]string, 0, len(m.suggest.Matches)+1)
	header := m.th.Style(theme.RoleMuted).Render(m.text(MsgSuggestHeader, MessageArgs{
		"title":    title,
		"previous": m.keys.label(KeyContextSuggestion, ActionSuggestionPrevious),
		"next":     m.keys.label(KeyContextSuggestion, ActionSuggestionNext),
		"accept":   m.keys.label(KeyContextSuggestion, ActionSuggestionAccept),
		"dismiss":  m.keys.label(KeyContextSuggestion, ActionSuggestionDismiss),
	}))
	lines = append(lines, fitRenderedLine(header, maxInt(m.width, 1)))
	prefixChar := "@"
	if m.suggest.Kind == mentionCommand {
		prefixChar = "/"
	}
	for i, match := range m.suggest.Matches {
		marker := "  "
		style := m.th.Style(theme.RoleText)
		if i == m.suggest.Selected {
			marker = "> "
			style = m.th.Style(theme.RoleTitle)
		}
		line := marker + prefixChar + match
		if i < len(m.suggest.Details) && m.suggest.Details[i] != "" {
			line += "  " + m.suggest.Details[i]
		}
		lines = append(lines, fitRenderedLine(style.Render(line), maxInt(m.width, 1)))
	}
	return lines
}

func (m *Model) visibleSuggestPanelLines(limit int) []string {
	all := m.suggestPanelLines()
	if limit <= 0 || len(all) == 0 {
		return nil
	}
	if len(all) <= limit {
		return all
	}
	selected := clampInt(m.suggest.Selected, 0, len(m.suggest.Matches)-1) + 1
	if limit == 1 {
		return []string{all[selected]}
	}
	slots := limit - 1
	start := selected - slots + 1
	if start < 1 {
		start = 1
	}
	maxStart := len(all) - slots
	if start > maxStart {
		start = maxStart
	}
	return append([]string{all[0]}, all[start:start+slots]...)
}

func (m *Model) pastePanelLines() []string {
	total := len(m.pendingPrefix) + len(m.pendingPaste)
	if total == 0 {
		return nil
	}
	lines := []string{m.th.Style(theme.RoleMuted).Render(m.text(MsgPasteHeader, MessageArgs{
		"undo": m.keys.label(KeyContextComposer, ActionComposerUndo),
	}))}
	start := maxInt(total-3, 0)
	if start > 0 {
		lines = append(lines, m.th.Style(theme.RoleMuted).Render(m.countText(MsgPasteEarlier, start, nil)))
	}
	for i := start; i < total; i++ {
		kind := m.text(MsgPasteKindPaste, nil)
		content := ""
		if i < len(m.pendingPrefix) {
			kind = m.text(MsgPasteKindRestored, nil)
			content = m.pendingPrefix[i]
		} else {
			content = m.pendingPaste[i-len(m.pendingPrefix)]
		}
		count := strings.Count(content, "\n") + 1
		summary := strings.TrimSpace(sanitize(strings.Split(content, "\n")[0]))
		if summary == "" {
			summary = m.text(MsgPasteBlankFirstLine, nil)
		}
		line := m.text(MsgPasteItem, MessageArgs{
			"index": i + 1, "kind": kind, "lines": count,
			"chars": len([]rune(content)), "summary": summary,
		})
		lines = append(lines, m.th.Style(theme.RoleInfo).Render(line))
	}
	return lines
}

func (m *Model) queuePanelLines() []string {
	total := m.followUps.len()
	if total == 0 {
		return nil
	}
	lines := []string{m.th.Style(theme.RoleMuted).Render(m.countText(MsgQueueHeader, total, MessageArgs{
		"queue": m.keys.label(KeyContextComposer, ActionComposerQueue),
		"edit":  m.keys.label(KeyContextComposer, ActionComposerRecallQueue),
	}))}
	shown := minInt(total, 3)
	for i := 0; i < shown; i++ {
		draft := m.followUps.drafts[i]
		summary := strings.TrimSpace(sanitize(firstLine(draft.Text)))
		if summary == "" {
			summary = m.text(MsgQueuePastedContent, nil)
		}
		if len(draft.Paste) > 0 {
			summary += m.countText(MsgQueuePasteItems, len(draft.Paste), nil)
		}
		lines = append(lines, m.th.Style(theme.RoleInfo).Render(m.text(MsgQueueItem, MessageArgs{"index": i + 1, "summary": summary})))
	}
	return lines
}

func (m *Model) transcriptWidth() int {
	if m.width-2 > 0 {
		return m.width - 2
	}
	return 1
}

func (m *Model) taskTreeLines() []string {
	return m.tasks.lines(m, maxInt(m.width-2, 1), 4)
}

// banner returns the degrade line shown while the daemon link is down —
// connection loss is a visible state with a remedy, never a silent freeze.
func (m *Model) banner() string {
	workspace := filepath.Base(filepath.Clean(m.workspaceRoot))
	if workspace == "." || workspace == string(filepath.Separator) || workspace == "" {
		workspace = "Carina"
	}
	switch m.conn {
	case ConnConnecting:
		return m.text(MsgConnecting, MessageArgs{"workspace": workspace})
	case ConnLost, ConnReconnecting:
		line := m.text(MsgConnectionUnavailable, MessageArgs{"workspace": workspace})
		if m.conn == ConnReconnecting {
			line += m.text(MsgReconnectAttempt, MessageArgs{"attempt": m.attempt})
		}
		return line
	default:
		return ""
	}
}

func (m *Model) borderStyle(border lipgloss.Border) lipgloss.Style {
	s := lipgloss.NewStyle().Border(border)
	if c := m.th.Color(theme.RoleBorder); c != nil {
		s = s.BorderForeground(c)
	}
	return s
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	// Update normally calls layout whenever a section changes. Reconcile here
	// as a defensive boundary for direct model construction in embedders and
	// for asynchronous state transitions that change a conditional row.
	if next := m.calculateLayout(); next != m.root {
		m.layout()
	}
	l := m.root
	var b strings.Builder

	if l.showBanner {
		b.WriteString(fitRenderedLine(m.th.Style(theme.RoleWarning).Render(m.banner()), l.width))
	}
	if l.showBanner {
		b.WriteString("\n")
	}

	if taskLines := m.taskTreeLines(); l.taskLines > 0 {
		b.WriteString(strings.Join(taskLines[:l.taskLines], "\n"))
		b.WriteString("\n")
	}

	// Lip Gloss v2 Width is the final styled block width, including borders.
	// Passing the terminal width therefore produces a complete right edge
	// without exceeding the cell grid.
	frame := m.borderStyle(lipgloss.RoundedBorder()).Width(l.width)
	if l.showTranscript {
		var transcript string
		if m.sidePaneActive() {
			// Dual-pane Side UI: frozen main | live side (no side padding so
			// the split columns can use the full content width).
			transcript = m.dualPaneTranscriptView(l.width, maxInt(l.viewportHeight, 1))
		} else {
			transcript = m.vp.View()
			if l.width >= 3 {
				transcript = lipgloss.NewStyle().Width(l.width).Padding(0, 1).Render(transcript)
			}
		}
		b.WriteString(transcript)
		b.WriteString("\n")
	}
	if panelLines := m.visibleSuggestPanelLines(l.suggestLines); len(panelLines) > 0 {
		for i, line := range panelLines {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fitRenderedLine(line, l.width))
		}
		b.WriteString("\n")
	}
	if queueLines := m.queuePanelLines(); l.queueLines > 0 {
		for i, line := range queueLines[:l.queueLines] {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fitRenderedLine(line, l.width))
		}
		b.WriteString("\n")
	}
	if pasteLines := m.pastePanelLines(); l.pasteLines > 0 {
		for i, line := range pasteLines[:l.pasteLines] {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fitRenderedLine(line, l.width))
		}
		b.WriteString("\n")
	}
	if l.historyLines > 0 {
		b.WriteString(m.historySearchPanelLine(l.width))
		b.WriteString("\n")
	}
	if l.framed {
		b.WriteString(frame.Render(m.input.View()))
	} else {
		b.WriteString(m.input.View())
	}
	b.WriteString("\n")

	if l.showStatus {
		b.WriteString(m.statusFooterView(l.width))
	}

	content := fitViewBlock(strings.TrimSuffix(b.String(), "\n"), l.width, l.height, false)
	if m.historySearch != nil && l.historyLines == 0 {
		// In a one-row terminal the search prompt is more actionable than a
		// stale textarea preview. The accepted draft returns on Enter.
		content = m.historySearchPanelLine(l.width)
	}
	if m.question != nil {
		modal := fitViewBlock(m.questionOverlayView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height,
			lipgloss.Center, lipgloss.Center, modal)
	} else if m.approval != nil {
		modal := fitViewBlock(m.overlayView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height,
			lipgloss.Center, lipgloss.Center, modal)
	} else if m.planReview != nil {
		modal := fitViewBlock(m.planReviewOverlayView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height,
			lipgloss.Center, lipgloss.Center, modal)
	} else if m.checkpointPicker != nil {
		modal := fitViewBlock(m.checkpointPickerView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height,
			lipgloss.Center, lipgloss.Center, modal)
	} else if m.modelPicker != nil {
		modal := fitViewBlock(m.modelPickerView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height,
			lipgloss.Center, lipgloss.Center, modal)
	} else if m.sessionPicker != nil {
		modal := fitViewBlock(m.sessionPickerView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height, lipgloss.Center, lipgloss.Center, modal)
	} else if m.keymapEditor != nil {
		modal := fitViewBlock(m.keymapEditorView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height,
			lipgloss.Center, lipgloss.Center, modal)
	} else if m.settings != nil {
		modal := fitViewBlock(m.settingsOverlayView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height, lipgloss.Center, lipgloss.Center, modal)
	} else if m.helpOpen {
		modal := fitViewBlock(m.helpOverlayView(), l.width, l.height, true)
		content = lipgloss.Place(l.width, l.height,
			lipgloss.Center, lipgloss.Center, modal)
	} else if m.transcriptPager != nil {
		content = m.transcriptPagerView(l.width, l.height)
	}

	v := tea.NewView(content)
	v.AltScreen = !m.noAlternateScreen
	v.ReportFocus = true
	// Cell motion is the smallest terminal mouse mode that delivers wheel
	// events without claiming passive pointer movement. Mouse tracking makes
	// unmodified drag belong to the app; terminals conventionally retain native
	// text selection through their modifier-assisted selection gesture.
	v.MouseMode = tea.MouseModeCellMotion
	// Mouse messages already flow through Program.eventLoop into Model.Update.
	// Re-sending one from OnMouse creates a self-sustaining message/render loop
	// after the first click or wheel event.
	// A nil declared cursor makes Bubble Tea hide the terminal cursor. This is
	// intentional while an overlay owns input, and whenever a zero-sized host
	// has not supplied a usable cell grid yet (R21).
	if !m.helpOpen && m.settings == nil && m.question == nil && m.approval == nil && m.planReview == nil && m.transcriptPager == nil &&
		m.checkpointPicker == nil && m.modelPicker == nil && m.sessionPicker == nil && m.keymapEditor == nil &&
		m.editor == nil && m.width > 0 && m.height > 0 {
		if m.historySearch != nil {
			cursor := m.input.Cursor()
			if cursor == nil {
				cursor = tea.NewCursor(0, 0)
			}
			cursor.Blink = true
			cursor.Position.X = m.historySearchCursorX(l.width)
			cursor.Position.Y = 0
			if l.historyLines > 0 {
				cursor.Position.Y = l.historyY
			}
			v.Cursor = cursor
		} else if cursor := m.input.Cursor(); cursor != nil {
			// Let the terminal renderer blink the physical cursor without
			// enabling textarea's virtual-cursor redraw timer.
			cursor.Blink = true
			cursor.Position.X = clampInt(cursor.Position.X+l.inputX, 0, l.width-1)
			cursor.Position.Y = clampInt(cursor.Position.Y+l.inputY, 0, l.height-1)
			v.Cursor = cursor
		}
	}
	return v
}

// fitViewBlock is the final safety boundary for cells rendered by components
// that do not know the root terminal size. Modal degradation keeps the title
// and final action row; losing the middle is preferable to hiding the controls.
func fitViewBlock(content string, width, height int, modal bool) string {
	if width <= 0 || height <= 0 || content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		if modal {
			switch height {
			case 1:
				// Modal renderers end with an action/footer row followed by a
				// bottom border. In the last available cell, the action is the
				// only useful survivor.
				lines = []string{lines[len(lines)-2]}
			case 2:
				lines = []string{lines[0], lines[len(lines)-2]}
			default:
				tail := lines[len(lines)-2:]
				lines = append(lines[:height-2], tail...)
			}
		} else {
			lines = lines[:height]
		}
	}
	for i := range lines {
		lines[i] = fitRenderedLine(lines[i], width)
	}
	return strings.Join(lines, "\n")
}

// fitRenderedLine clips an already-rendered line by terminal cells while
// preserving its ANSI styling. Sanitization belongs at the data boundary;
// applying fitLine here would strip the theme's escape sequences as well.
func fitRenderedLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(line) <= width {
		return line
	}
	if width == 1 {
		return ansi.Truncate(line, 1, "")
	}
	return ansi.Truncate(line, width, "…")
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
