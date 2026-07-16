package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

// planReviewState is a Grok-style plan approval surface: scroll the plan file,
// then approve (exit plan mode), request changes (seed composer), or quit plan.
type planReviewState struct {
	Path    string
	Body    []string
	Scroll  int
	Mode    string // plan | build (informational)
	Busy    bool
	Error   string
	Empty   bool
}

func (m *Model) openPlanReview() {
	if m.approval != nil || m.question != nil || m.helpOpen || m.settings != nil {
		return
	}
	path := m.planFilePath()
	st := &planReviewState{
		Path: path,
		Mode: m.modeLabel(),
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
	switch key {
	case "a", "A", "enter":
		// Approve plan and leave plan mode.
		pr.Busy = true
		pr.Error = ""
		return m.approvePlanFromReview(), true
	case "s", "S", "r", "R":
		// Request changes: seed composer and return focus to chat.
		m.closePlanReview()
		seed := m.text(MsgPlanReviewReviseSeed, nil)
		m.input.SetValue(seed)
		m.input.CursorEnd()
		m.push(m.text(MsgPlanReviewRequestChanges, nil))
		return nil, true
	case "q", "Q":
		// Quit plan mode without approving (build mode, no approve_plan).
		pr.Busy = true
		return m.quitPlanModeFromReview(), true
	case "esc":
		m.closePlanReview()
		return nil, true
	case "up", "k":
		pr.Scroll--
	case "down", "j":
		pr.Scroll++
	case "pgup":
		pr.Scroll -= m.planReviewViewportHeight()
	case "pgdown", " ":
		pr.Scroll += m.planReviewViewportHeight()
	case "home":
		pr.Scroll = 0
	case "end":
		pr.Scroll = len(pr.Body)
	default:
		return nil, false
	}
	m.clampPlanReviewScroll()
	return nil, true
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
	reserved := 8
	if m.planReview != nil && m.planReview.Error != "" {
		reserved++
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
	var bodyLines []string
	for _, line := range pr.Body[start:end] {
		bodyLines = append(bodyLines, ansi.Hardwrap(line, width, true))
	}
	if len(bodyLines) == 0 {
		bodyLines = []string{m.text(MsgViewPlanEmpty, nil)}
	}
	footer := m.th.Style(theme.RoleMuted).Render(m.text(MsgPlanReviewFooter, nil))
	if pr.Busy {
		footer = m.th.Style(theme.RoleMuted).Render(m.text(MsgPlanReviewBusy, nil))
	}
	parts := []string{title, meta, "", strings.Join(bodyLines, "\n"), "", footer}
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
