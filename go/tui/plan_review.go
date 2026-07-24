package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

// planLineComment is a line/range note collected during plan review.
// Comments seed the composer on "request changes"; they do not mutate the plan
// file until the operator sends the revision turn.
type planLineComment struct {
	StartLine int // 1-based inclusive
	EndLine   int // 1-based inclusive
	Text      string
}

// planReviewState is the plan approval surface: scroll the plan file,
// comment on lines (c / m), then approve (exit plan mode), request changes
// (seed composer with comments), or quit plan.
type planReviewState struct {
	Path         string
	Body         []string
	Scroll       int
	Cursor       int    // absolute line index in Body (0-based)
	Mode         string // plan | build (informational)
	Busy         bool
	Error        string
	Empty        bool
	CommentMode  bool
	CommentDraft string
	MarkStart    int // 0-based absolute line, or -1 when unset
	Comments     []planLineComment
}

func (m *Model) openPlanReview() {
	if m.approval != nil || m.question != nil || m.helpOpen || m.settings != nil {
		return
	}
	path := m.planFilePath()
	st := &planReviewState{
		Path:      path,
		Mode:      m.modeLabel(),
		MarkStart: -1,
	}
	body, err := m.readPlanFile()
	if err != nil {
		st.Empty = true
		st.Body = []string{m.text(MsgViewPlanMissing, nil)}
	} else {
		trimmed := strings.TrimSpace(body)
		if trimmed == "" {
			st.Empty = true
			st.Body = []string{m.text(MsgViewPlanEmpty, nil)}
		} else {
			st.Body = strings.Split(trimmed, "\n")
		}
	}
	m.planReview = st
	m.layout()
}

func (m *Model) closePlanReview() {
	m.planReview = nil
	m.layout()
}

func (m *Model) planReviewKey(key string) (tea.Cmd, bool) {
	if m.planReview == nil {
		return nil, false
	}
	pr := m.planReview
	if pr.Busy {
		return nil, true
	}

	// Comment compose mode: capture printable text until enter/esc.
	if pr.CommentMode {
		return m.planReviewCommentKey(key)
	}

	switch key {
	case "a", "A", "enter":
		pr.Busy = true
		pr.Error = ""
		return m.approvePlanFromReview(), true
	case "s", "S", "r", "R":
		m.closePlanReviewWithComments()
		return nil, true
	case "q", "Q":
		pr.Busy = true
		return m.quitPlanModeFromReview(), true
	case "esc":
		m.closePlanReview()
		return nil, true
	case "c", "C":
		if pr.Empty {
			return nil, true
		}
		pr.CommentMode = true
		pr.CommentDraft = ""
		return nil, true
	case "m", "M":
		if pr.Empty {
			return nil, true
		}
		// Toggle the range mark at the cursor.
		if pr.MarkStart == pr.Cursor {
			pr.MarkStart = -1
		} else {
			pr.MarkStart = pr.Cursor
		}
		return nil, true
	case "up", "k":
		pr.Cursor--
		m.ensurePlanReviewCursorVisible()
	case "down", "j":
		pr.Cursor++
		m.ensurePlanReviewCursorVisible()
	case "pgup":
		pr.Cursor -= m.planReviewViewportHeight()
		m.ensurePlanReviewCursorVisible()
	case "pgdown", " ":
		pr.Cursor += m.planReviewViewportHeight()
		m.ensurePlanReviewCursorVisible()
	case "home":
		pr.Cursor = 0
		m.ensurePlanReviewCursorVisible()
	case "end":
		pr.Cursor = maxInt(len(pr.Body)-1, 0)
		m.ensurePlanReviewCursorVisible()
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) planReviewCommentKey(key string) (tea.Cmd, bool) {
	pr := m.planReview
	if pr == nil || !pr.CommentMode {
		return nil, false
	}
	switch key {
	case "esc":
		pr.CommentMode = false
		pr.CommentDraft = ""
		return nil, true
	case "enter":
		text := strings.TrimSpace(pr.CommentDraft)
		pr.CommentMode = false
		pr.CommentDraft = ""
		if text == "" {
			return nil, true
		}
		start, end := pr.commentRange()
		pr.Comments = append(pr.Comments, planLineComment{
			StartLine: start + 1,
			EndLine:   end + 1,
			Text:      text,
		})
		pr.MarkStart = -1
		return nil, true
	case "backspace":
		if pr.CommentDraft == "" {
			return nil, true
		}
		_, size := utf8.DecodeLastRuneInString(pr.CommentDraft)
		pr.CommentDraft = pr.CommentDraft[:len(pr.CommentDraft)-size]
		return nil, true
	default:
		// Accept single runes / short paste; ignore control chords.
		if key == "" || strings.HasPrefix(key, "ctrl+") || strings.HasPrefix(key, "alt+") {
			return nil, true
		}
		if key == "tab" || key == "up" || key == "down" || key == "left" || key == "right" {
			return nil, true
		}
		if utf8.RuneCountInString(key) == 1 || (len(key) > 1 && !strings.Contains(key, "+")) {
			pr.CommentDraft += key
		}
		return nil, true
	}
}

func (pr *planReviewState) commentRange() (start, end int) {
	start = pr.Cursor
	end = pr.Cursor
	if pr.MarkStart >= 0 {
		start = minInt(pr.MarkStart, pr.Cursor)
		end = maxInt(pr.MarkStart, pr.Cursor)
	}
	if len(pr.Body) == 0 {
		return 0, 0
	}
	if start < 0 {
		start = 0
	}
	if end >= len(pr.Body) {
		end = len(pr.Body) - 1
	}
	if start > end {
		start, end = end, start
	}
	return start, end
}

func (m *Model) closePlanReviewWithComments() {
	pr := m.planReview
	if pr == nil {
		return
	}
	seed := m.text(MsgPlanReviewReviseSeed, nil)
	if len(pr.Comments) > 0 {
		var b strings.Builder
		b.WriteString(seed)
		b.WriteString("\n")
		b.WriteString(m.text(MsgPlanReviewCommentsHeader, nil))
		b.WriteString("\n")
		for _, c := range pr.Comments {
			if c.StartLine == c.EndLine {
				fmt.Fprintf(&b, "- L%d: %s\n", c.StartLine, c.Text)
			} else {
				fmt.Fprintf(&b, "- L%d–L%d: %s\n", c.StartLine, c.EndLine, c.Text)
			}
		}
		seed = b.String()
	}
	m.closePlanReview()
	m.input.SetValue(seed)
	m.input.CursorEnd()
	m.push(m.text(MsgPlanReviewRequestChanges, nil))
}

func (m *Model) approvePlanFromReview() tea.Cmd {
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return planReviewDoneMsg{kind: "approve", err: errorsNew("daemon not connected")}
		}
		var out map[string]any
		err := call.Call("session.approve_plan", map[string]any{"session_id": sessionID}, &out)
		return planReviewDoneMsg{kind: "approve", err: err}
	}
}

func (m *Model) quitPlanModeFromReview() tea.Cmd {
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return planReviewDoneMsg{kind: "quit", err: errorsNew("daemon not connected")}
		}
		err := call.Call("session.plan_mode", map[string]any{"session_id": sessionID, "on": false}, nil)
		return planReviewDoneMsg{kind: "quit", err: err}
	}
}

type planReviewDoneMsg struct {
	kind string // approve | quit
	err  error
}

func (m *Model) handlePlanReviewDone(msg planReviewDoneMsg) {
	if m.planReview == nil {
		return
	}
	if msg.err != nil {
		m.planReview.Busy = false
		m.planReview.Error = msg.err.Error()
		m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}))
		return
	}
	m.closePlanReview()
	switch msg.kind {
	case "approve":
		m.mode = "build"
		m.push(m.text(MsgPlanReviewApproved, MessageArgs{"glyph": glyphOK(m.th)}))
	case "quit":
		m.mode = "build"
		m.push(m.text(MsgPlanReviewQuit, MessageArgs{"glyph": glyphNeutral(m.th)}))
	}
	m.layout()
}

func (m *Model) planReviewViewportHeight() int {
	reserved := 10
	if m.planReview != nil {
		if m.planReview.Error != "" {
			reserved++
		}
		if m.planReview.CommentMode {
			reserved += 2
		}
		if n := len(m.planReview.Comments); n > 0 {
			reserved += minInt(n, 3) + 1
		}
	}
	return maxInt(m.height-reserved, 1)
}

func (m *Model) clampPlanReviewScroll() {
	if m.planReview == nil {
		return
	}
	maxScroll := maxInt(len(m.planReview.Body)-m.planReviewViewportHeight(), 0)
	if m.planReview.Scroll < 0 {
		m.planReview.Scroll = 0
	}
	if m.planReview.Scroll > maxScroll {
		m.planReview.Scroll = maxScroll
	}
	if m.planReview.Cursor < 0 {
		m.planReview.Cursor = 0
	}
	if n := len(m.planReview.Body); n > 0 && m.planReview.Cursor >= n {
		m.planReview.Cursor = n - 1
	}
}

func (m *Model) ensurePlanReviewCursorVisible() {
	if m.planReview == nil {
		return
	}
	m.clampPlanReviewScroll()
	pr := m.planReview
	vh := m.planReviewViewportHeight()
	if pr.Cursor < pr.Scroll {
		pr.Scroll = pr.Cursor
	}
	if pr.Cursor >= pr.Scroll+vh {
		pr.Scroll = pr.Cursor - vh + 1
	}
	m.clampPlanReviewScroll()
}

func (m *Model) planReviewOverlayView() string {
	pr := m.planReview
	if pr == nil {
		return ""
	}
	width := maxInt(m.width-4, 20)
	title := m.th.Style(theme.RoleTitle).Render(m.text(MsgPlanReviewTitle, nil))
	meta := m.th.Style(theme.RoleMuted).Render(
		fmt.Sprintf("%s · %s", m.text(MsgViewPlanMode, MessageArgs{"mode": pr.Mode}), pr.Path),
	)
	vh := m.planReviewViewportHeight()
	m.clampPlanReviewScroll()
	start := pr.Scroll
	end := minInt(start+vh, len(pr.Body))
	cursorStyle := m.th.Style(theme.RoleTitle)
	markStyle := m.th.Style(theme.RoleWarning)
	var bodyLines []string
	for i := start; i < end; i++ {
		line := pr.Body[i]
		prefix := fmt.Sprintf("%4d  ", i+1)
		wrapped := ansi.Hardwrap(line, maxInt(width-6, 8), true)
		if i == pr.Cursor {
			wrapped = cursorStyle.Render("› " + wrapped)
			prefix = cursorStyle.Render(fmt.Sprintf("%4d ", i+1))
		} else if pr.MarkStart >= 0 {
			lo, hi := pr.commentRange()
			if i >= lo && i <= hi {
				wrapped = markStyle.Render("· " + wrapped)
			} else {
				wrapped = "  " + wrapped
			}
		} else {
			wrapped = "  " + wrapped
		}
		bodyLines = append(bodyLines, prefix+wrapped)
	}
	if len(bodyLines) == 0 {
		bodyLines = []string{m.text(MsgViewPlanEmpty, nil)}
	}
	footer := m.th.Style(theme.RoleMuted).Render(m.text(MsgPlanReviewFooter, nil))
	if pr.Busy {
		footer = m.th.Style(theme.RoleMuted).Render(m.text(MsgPlanReviewBusy, nil))
	} else if pr.CommentMode {
		footer = m.th.Style(theme.RoleTitle).Render(m.text(MsgPlanReviewCommentPrompt, MessageArgs{
			"draft": pr.CommentDraft,
		}))
	}
	parts := []string{title, meta, "", strings.Join(bodyLines, "\n"), "", footer}
	if n := len(pr.Comments); n > 0 {
		parts = append(parts, m.th.Style(theme.RoleMuted).Render(
			m.text(MsgPlanReviewCommentCount, MessageArgs{"count": n}),
		))
	}
	if pr.Error != "" {
		parts = append(parts, m.th.Style(theme.RoleError).Render(pr.Error))
	}
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.th.Color(theme.RoleBorder)).
		Padding(0, 1).
		Width(minInt(width+4, m.width)).
		Render(strings.Join(parts, "\n"))
	return frame
}
