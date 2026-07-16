package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

type sessionListItem struct {
	SessionID        string `json:"session_id"`
	Name             string `json:"name"`
	WorkspaceRoot    string `json:"workspace_root"`
	Status           string `json:"status"`
	ParentID         string `json:"parent_id"`
	ForkedFromTaskID string `json:"forked_from_task_id"`
	CreatedAt        string `json:"created_at"`
}

func (m *Model) sessionStatusLabel(status string) string {
	switch strings.ToLower(status) {
	case "active":
		return m.text(MsgSessionStatusActive, nil)
	case "paused":
		return m.text(MsgSessionStatusPaused, nil)
	case "closed":
		return m.text(MsgSessionStatusClosed, nil)
	default:
		return status
	}
}

func (m *Model) sessionAge(value string) string {
	created, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	d := time.Since(created)
	if d < time.Minute {
		return m.text(MsgSessionAgeNow, nil)
	}
	if d < time.Hour {
		return m.text(MsgSessionAgeMinutes, MessageArgs{"count": int(d.Minutes())})
	}
	if d < 24*time.Hour {
		return m.text(MsgSessionAgeHours, MessageArgs{"count": int(d.Hours())})
	}
	return m.text(MsgSessionAgeDays, MessageArgs{"count": int(d.Hours() / 24)})
}

type sessionPickerState struct {
	generation       uint64
	loading          bool
	loadError        bool
	selected, scroll int
	items            []sessionListItem
	status           string
}
type sessionListMsg struct {
	generation uint64
	items      []sessionListItem
	err        error
}
type sessionActionMsg struct {
	generation uint64
	action     string
	session    sessionListItem
	err        error
}

func (m *Model) sessionSwitchBlocker() (MessageID, bool) {
	switch {
	case !draftEmpty(m.currentDraft()):
		return MsgSessionSwitchDraft, true
	case m.inFlightTaskID != "":
		return MsgSessionSwitchTask, true
	case m.submitting != nil:
		return MsgSessionSwitchSubmission, true
	case m.retrySubmission != nil:
		return MsgSessionSwitchRetry, true
	case m.followUps.len() > 0:
		return MsgSessionSwitchQueue, true
	case m.approval != nil || m.question != nil:
		return MsgSessionSwitchGovernance, true
	case m.editor != nil:
		return MsgSessionSwitchEditor, true
	case m.goal != nil && m.goal.Status != "completed":
		return MsgSessionSwitchGoal, true
	default:
		return "", false
	}
}

func (m *Model) openSessionPicker() tea.Cmd {
	m.closeSuggest()
	m.sessionOpGen++
	s := &sessionPickerState{generation: m.sessionOpGen, loading: true, status: m.text(MsgSessionPickerLoading, nil)}
	m.sessionPicker = s
	m.layout()
	call, gen := m.call, s.generation
	return func() tea.Msg {
		if call == nil {
			return sessionListMsg{generation: gen, err: errors.New("daemon not connected")}
		}
		var out []sessionListItem
		err := call.Call("session.list", map[string]any{}, &out)
		return sessionListMsg{generation: gen, items: out, err: err}
	}
}

func (m *Model) handleSessionList(msg sessionListMsg) {
	s := m.sessionPicker
	if s == nil || s.generation != msg.generation {
		return
	}
	s.loading = false
	if msg.err != nil {
		s.loadError = true
		s.status = m.text(MsgSessionPickerFailed, MessageArgs{"error": msg.err.Error()})
		return
	}
	s.loadError = false
	s.items = nil
	for _, item := range msg.items {
		if item.SessionID != m.sessionID && item.Status != "closed" {
			s.items = append(s.items, item)
		}
	}
	if len(s.items) == 0 {
		s.status = m.text(MsgSessionPickerEmpty, nil)
	} else {
		s.status = m.text(MsgSessionPickerHelp, nil)
	}
	s.clamp(m.sessionPickerPageHeight())
}

func (m *Model) sessionPickerPageHeight() int { return maxInt(m.height-9, 1) }
func (s *sessionPickerState) clamp(page int) {
	if len(s.items) == 0 {
		s.selected, s.scroll = 0, 0
		return
	}
	s.selected = clampInt(s.selected, 0, len(s.items)-1)
	if s.selected < s.scroll {
		s.scroll = s.selected
	}
	if s.selected >= s.scroll+page {
		s.scroll = s.selected - page + 1
	}
	s.scroll = clampInt(s.scroll, 0, maxInt(len(s.items)-page, 0))
}

func (m *Model) sessionPickerKey(key string) (tea.Cmd, bool) {
	s := m.sessionPicker
	if s == nil {
		return nil, false
	}
	switch key {
	case "esc":
		if m.sessionActionPending != "" || m.pendingSessionID != "" {
			s.status = m.text(MsgSessionActionResolving, nil)
			return nil, true
		}
		m.sessionPicker = nil
		m.layout()
		return m.resumeQueuedAfterTransient(), true
	case "r":
		if m.pendingSessionID != "" {
			if m.switchSession != nil {
				_ = m.switchSession(m.pendingSessionID)
			}
			s.loading = true
			s.status = m.text(MsgSessionSwitching, MessageArgs{"session": m.pendingSessionID})
			return nil, true
		}
		if !s.loading && (s.loadError || len(s.items) == 0) {
			return m.openSessionPicker(), true
		}
	case "b":
		if m.pendingSessionID != "" && m.previousSessionID != "" {
			target := m.previousSessionID
			if err := m.submissions.transfer(target); err != nil {
				s.status = m.text(MsgSessionSwitchLeaseBlocked, MessageArgs{"error": err.Error()})
				return nil, true
			}
			m.pendingSessionID = target
			m.pendingWorkspaceRoot = m.previousWorkspaceRoot
			if m.switchSession != nil {
				_ = m.switchSession(target)
			}
			s.loading = true
			s.status = m.text(MsgSessionSwitching, MessageArgs{"session": target})
			return nil, true
		}
	case "up", "k":
		s.selected--
	case "down", "j":
		s.selected++
	case "enter":
		if s.loading || len(s.items) == 0 {
			return nil, true
		}
		return m.resumeSession(s.items[s.selected].SessionID), true
	}
	s.clamp(m.sessionPickerPageHeight())
	return nil, true
}

func (m *Model) sessionPickerView() string {
	s := m.sessionPicker
	if s == nil {
		return ""
	}
	width := maxInt(m.width-4, 1)
	lines := []string{m.th.Style(theme.RoleTitle).Render(m.text(MsgSessionPickerTitle, nil)), ""}
	if s.loading {
		lines = append(lines, s.status)
	} else {
		page := m.sessionPickerPageHeight()
		s.clamp(page)
		end := minInt(s.scroll+page, len(s.items))
		for i := s.scroll; i < end; i++ {
			it := s.items[i]
			prefix := "  "
			if i == s.selected {
				prefix = "> "
			}
			name := it.Name
			if name == "" {
				name = it.SessionID
			}
			workspace := filepath.Base(filepath.Clean(it.WorkspaceRoot))
			label := name + "  " + m.sessionStatusLabel(it.Status)
			if workspace != "." && workspace != string(filepath.Separator) && workspace != "" {
				label += "  " + workspace
			}
			if age := m.sessionAge(it.CreatedAt); age != "" {
				label += "  " + age
			}
			if width >= 40 && it.ParentID != "" {
				label += "  " + m.text(MsgSessionPickerForkOf, MessageArgs{"parent": it.ParentID})
				if it.ForkedFromTaskID != "" {
					label += " " + m.text(MsgSessionPickerForkTask, MessageArgs{"task": it.ForkedFromTaskID})
				}
			}
			line := fitRenderedLine(prefix+label, width)
			if i == s.selected {
				line = m.th.Style(theme.RoleTitle).Render(line)
			}
			lines = append(lines, line)
		}
		lines = append(lines, "", fitRenderedLine(s.status, width))
	}
	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if c := m.th.Color(theme.RoleTitle); c != nil {
		style = style.BorderForeground(c)
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m *Model) beginSessionAction(action, method string, params map[string]any) tea.Cmd {
	if m.sessionActionPending != "" || m.pendingSessionID != "" {
		m.push(m.text(MsgSessionActionResolving, nil))
		return nil
	}
	if blocker, blocked := m.sessionSwitchBlocker(); blocked {
		m.push(m.text(MsgSessionSwitchBlocked, MessageArgs{"reason": m.text(blocker, nil)}))
		return nil
	}
	m.sessionOpGen++
	m.sessionActionPending = action
	gen := m.sessionOpGen
	call := m.call
	return func() tea.Msg {
		if call == nil {
			return sessionActionMsg{generation: gen, action: action, err: errors.New("daemon not connected")}
		}
		var out sessionListItem
		err := call.Call(method, params, &out)
		return sessionActionMsg{generation: gen, action: action, session: out, err: err}
	}
}
func (m *Model) newSession() tea.Cmd {
	return m.newSessionWithProfile("safe-edit")
}
func (m *Model) newSessionWithProfile(profile string) tea.Cmd {
	return m.beginSessionAction("new", "session.create", map[string]any{"workspace_root": m.workspaceRoot, "profile": profile})
}
func (m *Model) forkSession(taskID string) tea.Cmd {
	p := map[string]any{"session_id": m.sessionID}
	if taskID != "" {
		p["last_task_id"] = taskID
	}
	return m.beginSessionAction("fork", "session.fork", p)
}
func (m *Model) resumeSession(id string) tea.Cmd {
	return m.beginSessionAction("resume", "session.resume", map[string]any{"session_id": id})
}
func (m *Model) handleSessionAction(msg sessionActionMsg) {
	if msg.generation != m.sessionOpGen {
		return
	}
	m.sessionActionPending = ""
	if msg.err != nil {
		m.pendingSideQuestion = ""
		m.push(m.text(MsgSessionActionFailed, MessageArgs{"error": msg.err.Error()}))
		return
	}
	if msg.session.SessionID == "" {
		m.push(m.text(MsgSessionActionInvalid, nil))
		return
	}
	if m.switchSession == nil {
		m.push(m.text(MsgSessionSwitchUnavailable, nil))
		return
	}
	oldSession := m.sessionID
	m.previousSessionID, m.previousWorkspaceRoot = oldSession, m.workspaceRoot
	if err := m.submissions.transfer(msg.session.SessionID); err != nil {
		m.push(m.text(MsgSessionSwitchLeaseBlocked, MessageArgs{"error": err.Error()}))
		return
	}
	if err := m.switchSession(msg.session.SessionID); err != nil {
		_ = m.submissions.transfer(oldSession)
		m.push(m.text(MsgSessionSwitchFailed, MessageArgs{"error": err.Error()}))
		return
	}
	m.pendingSessionID = msg.session.SessionID
	m.pendingWorkspaceRoot = msg.session.WorkspaceRoot
	if m.sessionPicker == nil {
		m.sessionPicker = &sessionPickerState{generation: m.sessionOpGen}
	}
	m.sessionPicker.loading = true
	m.sessionPicker.status = m.text(MsgSessionSwitching, MessageArgs{"session": msg.session.SessionID})
	m.push(m.text(MsgSessionSwitching, MessageArgs{"session": msg.session.SessionID}))
	m.layout()
}
