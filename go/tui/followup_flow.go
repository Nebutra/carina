package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

func draftEmpty(draft promptDraft) bool {
	return strings.TrimSpace(draft.Text) == "" && len(draft.Paste) == 0
}

func (m *Model) enqueueFollowUp() bool {
	if m.inFlightTaskID == "" || m.submitting != nil || m.editor != nil {
		return false
	}
	draft := m.currentDraft()
	if draftEmpty(draft) {
		return true
	}
	m.followUps.enqueue(draft)
	m.clearComposerDraft()
	m.push(m.th.Style(theme.RoleMuted).Render(fmt.Sprintf("- queued follow-up %d", m.followUps.len())))
	m.layout()
	return true
}

func (m *Model) recallLastFollowUp() bool {
	if m.submitting != nil || m.editor != nil {
		return false
	}
	draft, ok := m.followUps.popBack()
	if !ok {
		return false
	}
	if current := m.currentDraft(); !draftEmpty(current) {
		// Swap instead of overwrite: the active composer becomes the newest
		// queued draft, so Alt+Up is lossless even mid-edit.
		m.followUps.enqueue(current)
	}
	m.restoreDraft(draft)
	m.push(m.th.Style(theme.RoleMuted).Render("- recalled latest follow-up for editing"))
	m.layout()
	return true
}

func (m *Model) clearComposerDraft() {
	m.input.Reset()
	m.pendingPaste = nil
	if m.suggest != nil {
		m.closeSuggest()
	}
	m.layout()
}

func mergeDraftsForRestore(drafts []promptDraft, current promptDraft) promptDraft {
	parts := make([]string, 0, len(drafts)+1)
	pastes := make([]string, 0)
	appendDraft := func(draft promptDraft) {
		if draft.Text != "" {
			parts = append(parts, draft.Text)
		}
		pastes = append(pastes, draft.Paste...)
	}
	for _, draft := range drafts {
		appendDraft(draft)
	}
	appendDraft(current)
	return promptDraft{Text: strings.Join(parts, "\n"), Paste: pastes}
}

func (m *Model) restoreQueuedDrafts(reason string) {
	if m.editor != nil {
		m.queueRestoreReason = reason
		return
	}
	drafts := m.followUps.drain()
	if len(drafts) == 0 {
		return
	}
	merged := mergeDraftsForRestore(drafts, m.currentDraft())
	m.restoreDraft(merged)
	m.push(m.th.Style(theme.RoleMuted).Render(fmt.Sprintf("- restored %d queued follow-up(s) after %s", len(drafts), reason)))
	m.layout()
}

func (m *Model) maybeSubmitNextQueued() tea.Cmd {
	if m.followUps.len() == 0 || m.inFlightTaskID != "" || m.submitting != nil ||
		m.approval != nil || m.question != nil || m.editor != nil {
		return nil
	}
	for m.followUps.len() > 0 {
		draft, ok := m.followUps.front()
		if !ok {
			return nil
		}
		text := strings.TrimSpace(draft.Text)
		if len(draft.Paste) == 0 && strings.HasPrefix(text, "!") {
			command := strings.TrimSpace(strings.TrimPrefix(text, "!"))
			if command == "" {
				m.restoreQueuedDrafts("invalid queued shell command")
				m.push(fmt.Sprintf("%s queued shell command is empty; drafts restored", glyphFailed(m.th)))
				return nil
			}
			return m.beginSubmissionSource(submissionShell, command, draft, true)
		}
		if len(draft.Paste) == 0 && strings.HasPrefix(text, "/") {
			if !safeQueuedSlash(text) {
				m.restoreQueuedDrafts("interactive queued command")
				m.push(m.th.Style(theme.RoleMuted).Render("- queued slash command restored; review and run it from the composer"))
				return nil
			}
			queued, _ := m.followUps.popFront()
			m.recordHistory(queued)
			m.historyPos = len(m.history)
			_ = m.slashCommand(text) // safeQueuedSlash commands are synchronous.
			m.layout()
			continue
		}
		if m.call == nil {
			m.restoreQueuedDrafts("automatic submission failure")
			m.push(fmt.Sprintf("%s automatic follow-up submission failed: daemon not connected", glyphFailed(m.th)))
			return nil
		}
		return m.beginSubmissionSource(submissionTask, "", draft, true)
	}
	return nil
}

func safeQueuedSlash(text string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	switch strings.TrimPrefix(parts[0], "/") {
	case "help", "keys", "recap":
		return len(parts) == 1
	case "search":
		return len(parts) >= 2
	default:
		return false
	}
}

func taskTerminalResult(ev map[string]any) (terminal, successful bool) {
	typ := strings.ToLower(str(ev["type"]))
	payload, _ := ev["payload"].(map[string]any)
	status := strings.ToLower(firstValue(ev, "status", "outcome"))
	if status == "" {
		status = strings.ToLower(firstValue(payload, "status", "outcome"))
	}
	failure := strings.Contains(status, "fail") || strings.Contains(status, "cancel") ||
		strings.Contains(status, "denied") || strings.Contains(status, "abort")
	switch typ {
	case "task.completed", "taskcomplete", "taskcompleted":
		return true, !failure
	case "task.failed", "task.cancelled", "task.canceled":
		return true, false
	default:
		return false, false
	}
}

func (m *Model) handleTaskTerminalEvent(ev map[string]any) tea.Cmd {
	terminal, successful := taskTerminalResult(ev)
	if !terminal {
		return nil
	}
	id := str(ev["task_id"])
	if id == "" {
		if payload, ok := ev["payload"].(map[string]any); ok {
			id = str(payload["task_id"])
		}
	}
	if m.inFlightTaskID == "" || id != m.inFlightTaskID {
		return nil
	}
	m.inFlightTaskID = ""
	if !successful {
		m.restoreQueuedDrafts("task failure")
		return nil
	}
	return m.maybeSubmitNextQueued()
}
