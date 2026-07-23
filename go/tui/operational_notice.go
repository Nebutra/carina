package tui

import (
	"strings"

	"github.com/Nebutra/carina/go/tui/theme"
)

func (m *Model) setOperationalNotice(text string, role theme.Role) {
	m.setOperationalNoticeKind("lifecycle", text, role)
}

func (m *Model) setOperationalNoticeKind(kind, text string, role theme.Role) {
	m.operationalNotice = operationalNotice{Kind: strings.TrimSpace(kind), Text: strings.TrimSpace(text), Role: role}
}

func (m *Model) clearOperationalNotice() {
	m.operationalNotice = operationalNotice{}
}

func (m *Model) clearOperationalNoticeKind(kind string) {
	if m.operationalNotice.Kind == strings.TrimSpace(kind) {
		m.clearOperationalNotice()
	}
}
