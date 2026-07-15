package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

type checkpointInfo struct {
	CheckpointID   string   `json:"checkpoint_id"`
	TaskID         string   `json:"task_id"`
	Turn           int      `json:"turn"`
	Summary        string   `json:"summary"`
	AppliedPatches []string `json:"applied_patches"`
}

type checkpointPreview struct {
	Checkpoint        checkpointInfo `json:"checkpoint"`
	ConversationTurns int            `json:"conversation_turns"`
	Summary           string         `json:"summary"`
	RollbackPatches   []string       `json:"rollback_patches"`
	WillResume        string         `json:"will_resume"`
}

type checkpointPickerState struct {
	generation   int
	items        []checkpointInfo
	selected     int
	scroll       int
	loading      bool
	preview      *checkpointPreview
	confirmArmed bool
	restoring    bool
	restoreError string
	restored     *checkpointRestoreResult
	resuming     bool
	resumeError  string
	status       string
}

type checkpointRestoreResult struct {
	CheckpointID string
	TaskID       string
	Turn         int
}

type checkpointListMsg struct {
	generation int
	items      []checkpointInfo
	err        error
}

type checkpointPreviewMsg struct {
	generation int
	preview    checkpointPreview
	err        error
}

type checkpointRestoreMsg struct {
	generation   int
	checkpointID string
	taskID       string
	turn         int
	err          error
}

type checkpointResumeMsg struct {
	generation int
	taskID     string
	status     string
	err        error
}

func (m *Model) openCheckpointPicker() tea.Cmd {
	m.closeSuggest()
	state := &checkpointPickerState{generation: 1, loading: true, status: m.text(MsgCheckpointLoading, nil)}
	m.checkpointPicker = state
	m.rewindPrimed = false
	m.layout()
	call, sessionID, generation := m.call, m.sessionID, state.generation
	return func() tea.Msg {
		if call == nil {
			return checkpointListMsg{generation: generation, err: fmt.Errorf("daemon not connected")}
		}
		var items []checkpointInfo
		err := call.Call("session.checkpoint.list", map[string]any{"session_id": sessionID}, &items)
		return checkpointListMsg{generation: generation, items: items, err: err}
	}
}

func (m *Model) closeCheckpointPicker() {
	if state := m.checkpointPicker; state != nil && (state.restoring || state.resuming) {
		if state.resuming {
			state.status = m.text(MsgCheckpointWaitResume, nil)
		} else {
			state.status = m.text(MsgCheckpointWaitRestore, nil)
		}
		return
	}
	m.checkpointPicker = nil
	m.layout()
}

func (m *Model) handleCheckpointList(msg checkpointListMsg) {
	state := m.checkpointPicker
	if state == nil || msg.generation != state.generation {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.status = m.text(MsgCheckpointListFailed, MessageArgs{"error": msg.err.Error()})
		return
	}
	state.items = msg.items
	state.selected = len(state.items) - 1
	state.status = ""
	if len(state.items) == 0 {
		state.status = m.text(MsgCheckpointNone, nil)
	}
	state.clamp(m.checkpointPickerPageHeight())
}

func (m *Model) handleCheckpointPreview(msg checkpointPreviewMsg) {
	state := m.checkpointPicker
	if state == nil || msg.generation != state.generation {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.status = m.text(MsgCheckpointPreviewFailed, MessageArgs{"error": msg.err.Error()})
		return
	}
	state.preview = &msg.preview
	state.confirmArmed = false
	state.status = m.text(MsgCheckpointReview, MessageArgs{
		"arm":     primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewArm)),
		"confirm": primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm)),
	})
}

func (m *Model) handleCheckpointRestore(msg checkpointRestoreMsg) {
	state := m.checkpointPicker
	if state == nil || msg.generation != state.generation {
		return
	}
	state.restoring = false
	if msg.err != nil {
		state.confirmArmed = false
		state.restoreError = msg.err.Error()
		state.status = m.text(MsgCheckpointRestoreFailed, MessageArgs{
			"error": msg.err.Error(),
			"retry": primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewRetry)),
		})
		return
	}
	state.restoreError = ""
	state.restored = &checkpointRestoreResult{
		CheckpointID: msg.checkpointID,
		TaskID:       msg.taskID,
		Turn:         msg.turn,
	}
	m.pausedRestore = state.restored
	state.status = m.text(MsgCheckpointRestoredStatus, nil)
	m.tasks.setTask(msg.taskID, "paused")
	if m.inFlightTaskID == msg.taskID {
		m.inFlightTaskID = ""
	}
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgCheckpointRestoredLog, MessageArgs{
		"checkpoint": msg.checkpointID, "turn": msg.turn, "task": msg.taskID,
	})))
	m.layout()
}

func (m *Model) handleCheckpointResume(msg checkpointResumeMsg) {
	state := m.checkpointPicker
	if state == nil || msg.generation != state.generation || state.restored == nil ||
		msg.taskID != state.restored.TaskID {
		return
	}
	state.resuming = false
	if msg.err != nil {
		state.resumeError = msg.err.Error()
		state.status = m.text(MsgCheckpointResumeFailed, MessageArgs{
			"error": msg.err.Error(),
			"retry": primaryKeyLabel(m.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredResume)),
		})
		return
	}
	status := normalizeTaskStatus(msg.status)
	if msg.status == "" {
		status = "running"
	}
	m.tasks.setTask(msg.taskID, status)
	if status != "paused" && !terminalTaskStatus(status) {
		m.inFlightTaskID = msg.taskID
	}
	if m.pausedRestore != nil && m.pausedRestore.TaskID == msg.taskID {
		m.pausedRestore = nil
	}
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgCheckpointResumedLog, MessageArgs{"task": msg.taskID})))
	m.closeCheckpointPicker()
}

func (m *Model) checkpointPickerKey(key string) (tea.Cmd, bool) {
	state := m.checkpointPicker
	if state == nil {
		return nil, false
	}
	if state.restoring {
		if m.keys.matches(KeyContextCheckpointPreview, ActionCheckpointPreviewClose, key) {
			state.status = m.text(MsgCheckpointWaitRestore, nil)
		}
		return nil, true
	}
	if state.resuming {
		if m.keys.matches(KeyContextCheckpointRestored, ActionCheckpointRestoredClose, key) {
			state.status = m.text(MsgCheckpointWaitResume, nil)
		}
		return nil, true
	}
	if state.restored != nil {
		switch {
		case m.keys.matches(KeyContextCheckpointRestored, ActionCheckpointRestoredResume, key):
			return m.resumeRestoredTask(), true
		case m.keys.matches(KeyContextCheckpointRestored, ActionCheckpointRestoredClose, key):
			m.closeCheckpointPicker()
			return m.resumeQueuedAfterTransient(), true
		default:
			return nil, true
		}
	}
	if state.preview != nil {
		if m.keys.matches(KeyContextCheckpointPreview, ActionCheckpointPreviewClose, key) {
			m.closeCheckpointPicker()
			return m.resumeQueuedAfterTransient(), true
		}
		if state.restoreError != "" {
			switch {
			case m.keys.matches(KeyContextCheckpointPreview, ActionCheckpointPreviewRetry, key),
				m.keys.matches(KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm, key):
				return m.restoreCheckpoint(*state.preview), true
			case m.keys.matches(KeyContextCheckpointPreview, ActionCheckpointPreviewBack, key):
				state.preview = nil
				state.restoreError = ""
				state.status = ""
			}
			return nil, true
		}
		switch {
		case m.keys.matches(KeyContextCheckpointPreview, ActionCheckpointPreviewBack, key):
			state.preview = nil
			state.confirmArmed = false
			state.status = ""
		case m.keys.matches(KeyContextCheckpointPreview, ActionCheckpointPreviewArm, key):
			state.confirmArmed = true
			state.status = m.text(MsgCheckpointArmed, MessageArgs{
				"confirm": primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm)),
				"cancel":  primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewClose)),
			})
		case m.keys.matches(KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm, key):
			if !state.confirmArmed {
				state.status = m.text(MsgCheckpointArmFirst, MessageArgs{
					"arm": primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewArm)),
				})
				return nil, true
			}
			return m.restoreCheckpoint(*state.preview), true
		default:
			state.confirmArmed = false
			state.status = m.text(MsgCheckpointDisarmed, nil)
		}
		return nil, true
	}
	if m.keys.matches(KeyContextCheckpointList, ActionCheckpointListClose, key) {
		m.closeCheckpointPicker()
		return m.resumeQueuedAfterTransient(), true
	}
	if state.loading {
		return nil, true
	}
	if len(state.items) == 0 {
		return nil, true
	}
	switch {
	case m.keys.matches(KeyContextCheckpointList, ActionCheckpointListUp, key):
		state.selected--
	case m.keys.matches(KeyContextCheckpointList, ActionCheckpointListDown, key):
		state.selected++
	case m.keys.matches(KeyContextCheckpointList, ActionCheckpointListPageUp, key):
		state.selected -= m.checkpointPickerPageHeight()
	case m.keys.matches(KeyContextCheckpointList, ActionCheckpointListPageDown, key):
		state.selected += m.checkpointPickerPageHeight()
	case m.keys.matches(KeyContextCheckpointList, ActionCheckpointListTop, key):
		state.selected = 0
	case m.keys.matches(KeyContextCheckpointList, ActionCheckpointListBottom, key):
		state.selected = len(state.items) - 1
	case m.keys.matches(KeyContextCheckpointList, ActionCheckpointListPreview, key):
		return m.previewCheckpoint(state.items[state.selected]), true
	default:
		return nil, true
	}
	state.clamp(m.checkpointPickerPageHeight())
	return nil, true
}

func (m *Model) previewCheckpoint(item checkpointInfo) tea.Cmd {
	state := m.checkpointPicker
	state.generation++
	state.loading = true
	state.status = m.text(MsgCheckpointLoadingPreview, nil)
	call, sessionID, generation := m.call, m.sessionID, state.generation
	return func() tea.Msg {
		if call == nil {
			return checkpointPreviewMsg{generation: generation, err: fmt.Errorf("daemon not connected")}
		}
		var preview checkpointPreview
		err := call.Call("session.checkpoint.preview", map[string]any{
			"session_id": sessionID, "checkpoint_id": item.CheckpointID,
		}, &preview)
		return checkpointPreviewMsg{generation: generation, preview: preview, err: err}
	}
}

func (m *Model) restoreCheckpoint(preview checkpointPreview) tea.Cmd {
	state := m.checkpointPicker
	state.generation++
	state.restoring = true
	state.restoreError = ""
	state.resumeError = ""
	state.status = m.text(MsgCheckpointRestoring, nil)
	call, sessionID, generation := m.call, m.sessionID, state.generation
	return func() tea.Msg {
		if call == nil {
			return checkpointRestoreMsg{generation: generation, err: fmt.Errorf("daemon not connected")}
		}
		var out struct {
			CheckpointID string `json:"checkpoint_id"`
			TaskID       string `json:"task_id"`
			Turn         int    `json:"turn"`
		}
		err := call.Call("session.checkpoint.restore", map[string]any{
			"session_id": sessionID, "checkpoint_id": preview.Checkpoint.CheckpointID, "confirmed": true,
		}, &out)
		checkpointID, taskID, turn := out.CheckpointID, out.TaskID, out.Turn
		if checkpointID == "" {
			checkpointID = preview.Checkpoint.CheckpointID
		}
		if taskID == "" {
			taskID = preview.Checkpoint.TaskID
		}
		if turn == 0 {
			turn = preview.Checkpoint.Turn
		}
		return checkpointRestoreMsg{generation: generation, checkpointID: checkpointID, taskID: taskID, turn: turn, err: err}
	}
}

func (m *Model) resumeRestoredTask() tea.Cmd {
	state := m.checkpointPicker
	if state == nil || state.restored == nil || state.restored.TaskID == "" || state.resuming {
		return nil
	}
	state.generation++
	state.resuming = true
	state.resumeError = ""
	state.status = m.text(MsgCheckpointResuming, nil)
	call, generation, taskID := m.call, state.generation, state.restored.TaskID
	return func() tea.Msg {
		if call == nil {
			return checkpointResumeMsg{generation: generation, taskID: taskID, err: fmt.Errorf("daemon not connected")}
		}
		var out struct {
			TaskID string `json:"task_id"`
			Status string `json:"status"`
		}
		err := call.Call("task.resume", map[string]any{"task_id": taskID}, &out)
		if out.TaskID != "" {
			taskID = out.TaskID
		}
		return checkpointResumeMsg{generation: generation, taskID: taskID, status: out.Status, err: err}
	}
}

func (m *Model) resumePausedRestore(explicitTaskID string) tea.Cmd {
	explicitTaskID = strings.TrimSpace(explicitTaskID)
	var restored checkpointRestoreResult
	if explicitTaskID != "" {
		restored = checkpointRestoreResult{TaskID: explicitTaskID}
	} else {
		if m.pausedRestore == nil || m.pausedRestore.TaskID == "" {
			m.push(m.text(MsgCheckpointNoRecent, MessageArgs{"glyph": glyphFailed(m.th)}))
			return nil
		}
		restored = *m.pausedRestore
	}
	if m.inFlightTaskID != "" && m.inFlightTaskID != restored.TaskID {
		m.push(m.text(MsgCheckpointOtherActive, MessageArgs{
			"glyph": glyphFailed(m.th), "task": restored.TaskID, "active": m.inFlightTaskID,
		}))
		return nil
	}
	m.closeSuggest()
	m.checkpointPicker = &checkpointPickerState{
		generation: 1,
		restored:   &restored,
		status:     m.text(MsgCheckpointPaused, nil),
	}
	m.layout()
	return m.resumeRestoredTask()
}

func (s *checkpointPickerState) clamp(page int) {
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

func (m *Model) checkpointPickerPageHeight() int { return maxInt(m.height-9, 1) }

func (m *Model) checkpointPickerView() string {
	state := m.checkpointPicker
	if state == nil {
		return ""
	}
	width := maxInt(m.width-8, 20)
	title := m.text(MsgCheckpointTitle, nil)
	if state.restored != nil {
		title = m.text(MsgCheckpointRestoredTitle, nil)
	}
	lines := []string{m.th.Style(theme.RoleWarning).Render(title)}
	if state.restored != nil {
		restored := state.restored
		if restored.CheckpointID == "" {
			title = m.text(MsgCheckpointResumeTitle, nil)
			lines[0] = m.th.Style(theme.RoleWarning).Render(title)
			lines = append(lines, "", fitRenderedLine(m.text(MsgCheckpointTaskLine, MessageArgs{"task": restored.TaskID}), width), "",
				m.text(MsgCheckpointExplicitTask, nil))
		} else {
			lines = append(lines, "",
				fitRenderedLine(m.text(MsgCheckpointRestoredLine, MessageArgs{"checkpoint": restored.CheckpointID, "turn": restored.Turn, "task": restored.TaskID}), width),
				"", m.text(MsgCheckpointContextRolledBack, nil),
				m.text(MsgCheckpointAuditRetained, nil),
				m.text(MsgCheckpointPausedNoAuto, nil))
		}
		switch {
		case state.resuming:
			lines = append(lines, "", m.text(MsgCheckpointResumeProgress, nil))
		case state.resumeError != "":
			lines = append(lines, "", m.text(MsgCheckpointRetryResumeActions, MessageArgs{
				"resume": primaryKeyLabel(m.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredResume)),
				"close":  primaryKeyLabel(m.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredClose)),
			}))
		default:
			lines = append(lines, "", m.text(MsgCheckpointResumeActions, MessageArgs{
				"resume": primaryKeyLabel(m.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredResume)),
				"close":  primaryKeyLabel(m.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredClose)),
			}))
		}
	} else if state.preview != nil {
		preview := state.preview
		summary := preview.Summary
		if summary == "" {
			summary = preview.Checkpoint.Summary
		}
		lines = append(lines, "",
			fitRenderedLine(m.countText(MsgCheckpointPreviewLine, preview.ConversationTurns, MessageArgs{"checkpoint": preview.Checkpoint.CheckpointID, "turn": preview.Checkpoint.Turn}), width),
			fitRenderedLine(summary, width), "", m.text(MsgCheckpointRollbackPatches, nil))
		if len(preview.RollbackPatches) == 0 {
			lines = append(lines, m.text(MsgCheckpointNoPatches, nil))
		} else {
			for _, patch := range preview.RollbackPatches {
				lines = append(lines, fitRenderedLine("  - "+patch, width))
			}
		}
		switch {
		case state.restoring:
			lines = append(lines, "", m.text(MsgCheckpointRestoreProgress, nil))
		case state.restoreError != "":
			lines = append(lines, "", m.text(MsgCheckpointRetryRestoreActions, MessageArgs{
				"retry": primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewRetry)),
				"back":  primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewBack)),
				"close": primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewClose)),
			}))
		default:
			lines = append(lines, "", m.text(MsgCheckpointRestoreActions, MessageArgs{
				"arm":     primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewArm)),
				"confirm": primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm)),
				"back":    primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewBack)),
				"close":   primaryKeyLabel(m.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewClose)),
			}))
		}
	} else {
		page := m.checkpointPickerPageHeight()
		state.clamp(page)
		end := minInt(state.scroll+page, len(state.items))
		for i := state.scroll; i < end; i++ {
			item := state.items[i]
			prefix := "  "
			if i == state.selected {
				prefix = "> "
			}
			summary := strings.TrimSpace(item.Summary)
			if summary == "" {
				summary = m.text(MsgCheckpointDefaultSummary, nil)
			}
			line := m.text(MsgCheckpointListItem, MessageArgs{"prefix": prefix, "turn": item.Turn, "summary": summary})
			if i == state.selected {
				line = m.th.Style(theme.RoleTitle).Render(line)
			}
			lines = append(lines, fitRenderedLine(line, width))
		}
		if len(state.items) > 0 {
			lines = append(lines, "", m.text(MsgCheckpointListActions, MessageArgs{
				"preview": primaryKeyLabel(m.keys.keys(KeyContextCheckpointList, ActionCheckpointListPreview)),
				"up":      primaryKeyLabel(m.keys.keys(KeyContextCheckpointList, ActionCheckpointListUp)),
				"down":    primaryKeyLabel(m.keys.keys(KeyContextCheckpointList, ActionCheckpointListDown)),
				"close":   primaryKeyLabel(m.keys.keys(KeyContextCheckpointList, ActionCheckpointListClose)),
			}))
		}
	}
	if state.status != "" {
		lines = append(lines, "", fitRenderedLine(m.th.Style(theme.RoleMuted).Render(state.status), width))
	}
	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if color := m.th.Color(theme.RoleWarning); color != nil {
		style = style.BorderForeground(color)
	}
	return style.Render(strings.Join(lines, "\n"))
}
