package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

// sidePaneState is a dual-pane Side UI for /btw --fork and /side.
//
// After session.fork the TUI switches to the child session (daemon contract).
// The primary session transcript is frozen into PrimaryLines so the operator
// still sees the main conversation while the side Q&A streams on the right.
// /side-close switches back to the primary session and clears the pane.
type sidePaneState struct {
	PrimarySessionID string
	PrimaryLines     []string
	Question         string
	SideSessionID    string
}

const sidePaneSnapshotMax = 80

func (m *Model) capturePrimarySnapshot() []string {
	lines := m.tr.lines
	if len(lines) == 0 {
		return []string{m.th.Style(theme.RoleMuted).Render("(empty main transcript)")}
	}
	if len(lines) > sidePaneSnapshotMax {
		lines = lines[len(lines)-sidePaneSnapshotMax:]
	}
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

// armSidePane snapshots the current session as the dual-pane main column
// before a fork switches the live session to the child.
func (m *Model) armSidePane(question string) {
	if m.sessionID == "" {
		return
	}
	m.sidePane = &sidePaneState{
		PrimarySessionID: m.sessionID,
		PrimaryLines:     m.capturePrimarySnapshot(),
		Question:         strings.TrimSpace(question),
	}
}

func (m *Model) noteSideSession(sessionID string) {
	if m.sidePane == nil {
		return
	}
	m.sidePane.SideSessionID = sessionID
}

func (m *Model) sidePaneActive() bool {
	return m.sidePane != nil && m.sidePane.PrimarySessionID != "" &&
		m.sessionID != "" && m.sessionID != m.sidePane.PrimarySessionID
}

// closeSidePane switches back to the primary session when possible and clears
// the dual-pane chrome. Safe to call when no side pane is armed.
func (m *Model) closeSidePane() tea.Cmd {
	sp := m.sidePane
	if sp == nil {
		m.push(m.text(MsgUpdateUsageSideClose, nil))
		return nil
	}
	primary := strings.TrimSpace(sp.PrimarySessionID)
	m.sidePane = nil
	if primary == "" || primary == m.sessionID {
		m.push(m.text(MsgSidePaneClosed, MessageArgs{
			"glyph": glyphOK(m.th), "session": m.sessionID,
		}))
		m.layout()
		return nil
	}
	if m.switchSession == nil {
		m.push(m.text(MsgSessionSwitchUnavailable, nil))
		return nil
	}
	if err := m.submissions.transfer(primary); err != nil {
		m.push(m.text(MsgSessionSwitchLeaseBlocked, MessageArgs{"error": err.Error()}))
		// Re-arm so the operator can retry /side-close.
		m.sidePane = &sidePaneState{PrimarySessionID: primary}
		return nil
	}
	old := m.sessionID
	m.pendingSessionID = primary
	if err := m.switchSession(primary); err != nil {
		_ = m.submissions.transfer(old)
		m.pendingSessionID = ""
		m.sidePane = &sidePaneState{PrimarySessionID: primary}
		m.push(m.text(MsgSessionSwitchFailed, MessageArgs{"error": err.Error()}))
		return nil
	}
	m.push(m.text(MsgSidePaneClosed, MessageArgs{
		"glyph": glyphOK(m.th), "session": primary,
	}))
	m.layout()
	return nil
}

// dualPaneTranscriptView renders main (frozen) | side (live viewport) when the
// side pane is active and the terminal is wide enough; otherwise falls back to
// the live transcript alone (narrow terminals).
func (m *Model) dualPaneTranscriptView(width, height int) string {
	if !m.sidePaneActive() || width < 60 || height < 3 {
		return m.vp.View()
	}
	gap := 1
	half := (width - gap) / 2
	if half < 20 {
		return m.vp.View()
	}
	rightW := width - half - gap
	titleMain := m.th.Style(theme.RoleMuted).Render(m.text(MsgSidePaneMain, nil) + " · " + shortID(m.sidePane.PrimarySessionID))
	titleSide := m.th.Style(theme.RoleTitle).Render(m.text(MsgSidePaneSide, nil) + " · " + shortID(m.sessionID))
	if q := strings.TrimSpace(m.sidePane.Question); q != "" {
		titleSide += m.th.Style(theme.RoleMuted).Render(" · " + ansi.Truncate(q, maxInt(rightW-12, 8), "…"))
	}

	leftBody := joinClipped(m.sidePane.PrimaryLines, half, height-1)
	rightBody := clipBlock(m.vp.View(), rightW, height-1)

	left := titleMain + "\n" + leftBody
	right := titleSide + "\n" + rightBody
	left = lipgloss.NewStyle().Width(half).MaxHeight(height).Render(left)
	right = lipgloss.NewStyle().Width(rightW).MaxHeight(height).Render(right)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), right)
}

func joinClipped(lines []string, width, height int) string {
	if height < 1 {
		height = 1
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(ansi.Truncate(ansi.Strip(line), width, "…"))
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String()
}

func clipBlock(content string, width, height int) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}
