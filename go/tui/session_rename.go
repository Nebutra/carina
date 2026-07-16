package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type sessionRenameMsg struct {
	sessionID  string
	generation uint64
	name       string
	err        error
}

func (m *Model) renameSession(name string) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		m.push(m.text(MsgSessionRenameUsage, nil))
		return nil
	}
	call, sid, gen := m.call, m.sessionID, m.sessionGeneration
	return func() tea.Msg {
		if call == nil {
			return sessionRenameMsg{sessionID: sid, generation: gen, name: name, err: errors.New("daemon not connected")}
		}
		var out sessionListItem
		err := call.Call("session.rename", map[string]any{"session_id": sid, "name": name}, &out)
		return sessionRenameMsg{sessionID: sid, generation: gen, name: name, err: err}
	}
}

func (m *Model) handleSessionRename(msg sessionRenameMsg) {
	if msg.sessionID != m.sessionID || msg.generation != m.sessionGeneration {
		return
	}
	if msg.err != nil {
		m.push(m.text(MsgSessionRenameFailed, MessageArgs{"error": msg.err.Error()}))
		return
	}
	m.push(m.text(MsgSessionRenamed, MessageArgs{"name": msg.name}))
}
