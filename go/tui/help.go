package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

func (m *Model) showHelp() {
	m.helpOpen = true
	m.helpScroll = 0
	m.lastCtrlC = time.Time{}
	m.ctrlCHint = ""
	m.layout()
}

func (m *Model) closeHelp() {
	m.helpOpen = false
	m.helpScroll = 0
	m.lastCtrlC = time.Time{}
	m.ctrlCHint = ""
	m.layout()
}

func (m *Model) helpBodyLines() []string {
	lines := []string{
		"Commands",
		"  /help                 commands and keybindings",
		"  /agents               available agent modes",
		"  /checkpoints          rewind points for this session",
		"  /search <text>         search visible transcript",
		"  /recap                 compact current-session recap",
		"  /mode <build|plan>     change interaction mode",
		"  !<command>             governed shell command",
		"  @<path|agent>          reference a path or agent",
		"",
		"Keybindings",
	}
	return append(lines, m.keys.helpLines()...)
}

func (m *Model) helpViewportHeight() int {
	// The frame consumes two border rows plus title, spacer, footer spacer,
	// and footer rows. Reserving all six keeps the scroll math aligned with
	// what is actually visible, including in very short terminals.
	return maxInt(m.height-6, 1)
}

func (m *Model) clampHelpScroll() {
	maxScroll := maxInt(len(m.helpBodyLines())-m.helpViewportHeight(), 0)
	m.helpScroll = clampInt(m.helpScroll, 0, maxScroll)
}

func (m *Model) helpKey(key string) (tea.Cmd, bool) {
	if !m.helpOpen {
		return nil, false
	}
	switch {
	case m.keys.matches(KeyContextPager, ActionPagerClose, key),
		m.keys.matches(KeyContextGlobal, ActionGlobalHelp, key):
		m.closeHelp()
	case m.keys.matches(KeyContextPager, ActionPagerUp, key):
		m.helpScroll--
	case m.keys.matches(KeyContextPager, ActionPagerDown, key):
		m.helpScroll++
	case m.keys.matches(KeyContextPager, ActionPagerPageUp, key):
		m.helpScroll -= m.helpViewportHeight()
	case m.keys.matches(KeyContextPager, ActionPagerPageDown, key):
		m.helpScroll += m.helpViewportHeight()
	case m.keys.matches(KeyContextPager, ActionPagerTop, key):
		m.helpScroll = 0
	case m.keys.matches(KeyContextPager, ActionPagerBottom, key):
		m.helpScroll = len(m.helpBodyLines())
	default:
		return nil, false
	}
	m.clampHelpScroll()
	return nil, true
}

func (m *Model) helpOverlayView() string {
	if !m.helpOpen {
		return ""
	}
	contentWidth := maxInt(m.width-8, 1)
	body := m.helpBodyLines()
	m.clampHelpScroll()
	start := m.helpScroll
	end := minInt(start+m.helpViewportHeight(), len(body))
	lines := []string{m.th.Style(theme.RoleWarning).Render("Carina help"), ""}
	for _, line := range body[start:end] {
		lines = append(lines, fitRenderedLine(line, contentWidth))
	}
	footer := fmt.Sprintf("[%s] close  [%s/%s] scroll",
		m.keys.label(KeyContextPager, ActionPagerClose),
		m.keys.label(KeyContextPager, ActionPagerUp),
		m.keys.label(KeyContextPager, ActionPagerDown),
	)
	if len(body) > m.helpViewportHeight() {
		footer += fmt.Sprintf("  %d-%d/%d", start+1, end, len(body))
	}
	lines = append(lines, "", fitRenderedLine(m.th.Style(theme.RoleMuted).Render(footer), contentWidth))
	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if color := m.th.Color(theme.RoleWarning); color != nil {
		style = style.BorderForeground(color)
	}
	return style.Render(strings.Join(lines, "\n"))
}
