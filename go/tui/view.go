package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/theme"
)

// layout recomputes component sizes. The prompt grows with its content but is
// capped so the transcript always keeps the majority of the terminal.
func (m *Model) layout() {
	statusHeight := 1
	bannerHeight := 1
	taskHeight := len(m.taskTreeLines())
	suggestHeight := len(m.suggestPanelLines())
	vw := m.width - 2
	if vw < 1 {
		vw = 1
	}
	iw := m.width - 8
	if iw < 1 {
		iw = 1
	}
	maxInputHeight := m.height / 3
	if maxInputHeight < 3 {
		maxInputHeight = 3
	}
	if maxInputHeight > 10 {
		maxInputHeight = 10
	}
	m.input.MaxHeight = maxInputHeight
	m.input.SetWidth(iw)
	inputHeight := m.input.Height() + 2                                                         // input border
	vh := m.height - inputHeight - statusHeight - bannerHeight - taskHeight - suggestHeight - 2 // transcript border
	if vh < 1 {
		vh = 1
	}
	m.vp.SetWidth(vw)
	m.vp.SetHeight(vh)
	m.tr.resizePresentations(m.th, m.transcriptWidth())
	m.vp.SetContentLines(m.tr.lines)
	if m.followTail {
		m.vp.GotoBottom()
	}
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
	title := "files"
	if m.suggest.Kind == mentionCommand {
		title = "commands"
	}
	lines := make([]string, 0, len(m.suggest.Matches)+1)
	lines = append(lines, m.th.Style(theme.RoleMuted).Render(fmt.Sprintf("%s (1-%d to pick, esc to dismiss)", title, len(m.suggest.Matches))))
	prefixChar := "@"
	if m.suggest.Kind == mentionCommand {
		prefixChar = "/"
	}
	for i, match := range m.suggest.Matches {
		lines = append(lines, fmt.Sprintf("  %d %s%s", i+1, prefixChar, match))
	}
	return lines
}

func (m *Model) transcriptWidth() int {
	if m.width-4 > 0 {
		return m.width - 4
	}
	return 1
}

func (m *Model) taskTreeLines() []string {
	return m.tasks.lines(m.th, maxInt(m.width-2, 1), 4)
}

// banner returns the degrade line shown while the daemon link is down —
// connection loss is a visible state with a remedy, never a silent freeze.
func (m *Model) banner() string {
	switch m.conn {
	case ConnLost, ConnReconnecting:
		line := microcopy.Degrade(microcopy.DegradeDaemonUnreachable,
			microcopy.Args{"socket": m.socket},
			microcopy.WithLocale(m.locale), microcopy.WithPlain(m.plain()))
		if m.conn == ConnReconnecting {
			// m.locale may be an unnormalized flag value ("zh-CN",
			// "zh_TW.UTF-8") — main.go only normalizes the DetectLocale
			// fallback, not an explicit --locale. Normalize here the same
			// way Governed/Degrade/Loading do internally, so the suffix
			// matches the (already-normalized) Degrade text it's appended
			// to instead of silently falling back to English.
			if microcopy.NormalizeLocale(m.locale) == "zh" {
				line += fmt.Sprintf("（正在重连，第 %d 次）", m.attempt)
			} else {
				line += fmt.Sprintf(" (reconnecting, attempt %d)", m.attempt)
			}
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
	var b strings.Builder

	if bn := m.banner(); bn != "" {
		b.WriteString(m.th.Style(theme.RoleWarning).Render(bn))
	}
	b.WriteString("\n")

	if taskLines := m.taskTreeLines(); len(taskLines) > 0 {
		b.WriteString(strings.Join(taskLines, "\n"))
		b.WriteString("\n")
	}

	frame := m.borderStyle(lipgloss.RoundedBorder()).Width(maxInt(m.width-2, 1))
	b.WriteString(frame.Render(m.vp.View()))
	b.WriteString("\n")
	if panelLines := m.suggestPanelLines(); len(panelLines) > 0 {
		b.WriteString(strings.Join(panelLines, "\n"))
		b.WriteString("\n")
	}
	b.WriteString(frame.Render(m.input.View()))
	b.WriteString("\n")

	status := "not attached"
	if m.sessionID != "" {
		status = "session " + m.sessionID
	}
	activity := "ready"
	if m.inFlightTaskID != "" {
		activity = "running " + m.inFlightTaskID
	}
	if m.unseenLines > 0 {
		activity += fmt.Sprintf(" · %d new", m.unseenLines)
	}
	statusLine := m.th.Style(theme.RoleMuted).Render(fmt.Sprintf(
		" carina · %s · mode %s · %s · %d lines", status, m.mode, activity, len(m.tr.lines)))
	b.WriteString(fitLine(statusLine, maxInt(m.width, 1)))

	content := b.String()
	if m.question != nil {
		content = lipgloss.Place(maxInt(m.width, 1), maxInt(m.height, 1),
			lipgloss.Center, lipgloss.Center, m.questionOverlayView())
	} else if m.approval != nil {
		// Spike-proven overlay: full-frame replacement via lipgloss.Place.
		// The v2 Layers API is still unexercised upstream (spike sharp edge);
		// revisit when the declared-cursor work (R21) lands.
		content = lipgloss.Place(maxInt(m.width, 1), maxInt(m.height, 1),
			lipgloss.Center, lipgloss.Center, m.overlayView())
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
