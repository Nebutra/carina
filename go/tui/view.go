package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/theme"
)

// layout recomputes component sizes. Widths are clamped: bubbles v2
// textinput panics on negative width before the first WindowSizeMsg (spike
// sharp edge).
func (m *Model) layout() {
	inputHeight := 3
	statusHeight := 1
	bannerHeight := 1
	vh := m.height - inputHeight - statusHeight - bannerHeight - 2 // transcript border
	if vh < 3 {
		vh = 3
	}
	vw := m.width - 2
	if vw < 20 {
		vw = 20
	}
	iw := m.width - 8
	if iw < 20 {
		iw = 20
	}
	m.vp.SetWidth(vw)
	m.vp.SetHeight(vh)
	m.input.SetWidth(iw)
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

	frame := m.borderStyle(lipgloss.RoundedBorder()).Width(maxInt(m.width-2, 20))
	b.WriteString(frame.Render(m.vp.View()))
	b.WriteString("\n")
	b.WriteString(frame.Render(" > " + m.input.View()))
	b.WriteString("\n")

	status := "not attached"
	if m.sessionID != "" {
		status = "session " + m.sessionID
	}
	b.WriteString(m.th.Style(theme.RoleMuted).Render(fmt.Sprintf(
		" carina · %s · %d lines · enter submit · ctrl+c cancel/exit", status, len(m.tr.lines))))

	content := b.String()
	if m.approval != nil {
		// Spike-proven overlay: full-frame replacement via lipgloss.Place.
		// The v2 Layers API is still unexercised upstream (spike sharp edge);
		// revisit when the declared-cursor work (R21) lands.
		content = lipgloss.Place(maxInt(m.width, 20), maxInt(m.height, 10),
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
