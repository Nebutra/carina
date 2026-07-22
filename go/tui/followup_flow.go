package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

func draftEmpty(draft promptDraft) bool {
	return strings.TrimSpace(draft.Text) == "" && len(draft.Prefix) == 0 && len(draft.Paste) == 0
}

func (m *Model) enqueueFollowUp() bool {
	if m.inFlightTaskID == "" || m.submitting != nil || m.editor != nil {
		return false
	}
	draft := m.currentDraft()
	if draftEmpty(draft) {
		return true
	}
	// Queue entries own the routing choice made when they were queued. A later
	// /model switch applies only to later work, not to an existing draft.
	draft.Model = m.model
	draft.ReasoningEffort = m.reasoningEffort
	draft.Mode = "background"
	if m.inShellMode() {
		// Persist sticky-shell intent as a leading ! for dequeue routing.
		if command, ok := shellCommandFromDraft(draft.Text, true); ok {
			draft.Text = historyTextForShell(command)
		}
	}
	m.followUps.enqueue(draft)
	m.clearComposerDraft()
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgFollowupQueued, MessageArgs{"count": m.followUps.len()})))
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
	m.resetComposerUndo()
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgFollowupRecalled, nil)))
	m.layout()
	return true
}

func (m *Model) clearComposerDraft() {
	m.input.Reset()
	m.pendingPrefix = nil
	m.pendingPaste = nil
	m.resetComposerUndo()
	if m.suggest != nil {
		m.closeSuggest()
	}
	m.layout()
}

func mergeDraftsForRestore(drafts []promptDraft, current promptDraft) promptDraft {
	prefix := make([]string, 0, len(drafts)+len(current.Prefix))
	for _, draft := range drafts {
		prefix = append(prefix, draftPrompt(draft))
	}
	prefix = append(prefix, current.Prefix...)
	return promptDraft{
		Prefix: prefix,
		Text:   current.Text,
		Paste:  append([]string(nil), current.Paste...),
	}
}

func (m *Model) restoreQueuedDrafts(reason string) {
	if m.editor != nil || m.historySearch != nil || m.checkpointPicker != nil || m.modelPicker != nil || m.sessionPicker != nil || m.keymapEditor != nil {
		m.queueRestoreReason = reason
		return
	}
	drafts := m.followUps.drain()
	if len(drafts) == 0 {
		return
	}
	merged := mergeDraftsForRestore(drafts, m.currentDraft())
	m.restoreDraft(merged)
	m.resetComposerUndo()
	m.push(m.th.Style(theme.RoleMuted).Render(m.countText(MsgFollowupRestored, len(drafts), nil)))
	m.layout()
}

func (m *Model) resumeQueuedAfterTransient() tea.Cmd {
	if m.queueRestoreReason != "" {
		reason := m.queueRestoreReason
		m.queueRestoreReason = ""
		m.restoreQueuedDrafts(reason)
		return nil
	}
	return m.maybeSubmitNextQueued()
}

func (m *Model) maybeSubmitNextQueued() tea.Cmd {
	if m.followUps.len() == 0 || m.inFlightTaskID != "" || m.submitting != nil ||
		m.approval != nil || m.question != nil || m.editor != nil || m.helpOpen ||
		m.historySearch != nil || m.transcriptPager != nil || m.checkpointPicker != nil || m.modelPicker != nil || m.sessionPicker != nil ||
		m.keymapEditor != nil || m.queueRecallPending || m.retrySubmission != nil {
		return nil
	}
	for m.followUps.len() > 0 {
		draft, ok := m.followUps.front()
		if !ok {
			return nil
		}
		text := strings.TrimSpace(draft.Text)
		if len(draft.Prefix) == 0 && len(draft.Paste) == 0 && strings.HasPrefix(text, "!") {
			command := strings.TrimSpace(strings.TrimPrefix(text, "!"))
			if command == "" {
				m.restoreQueuedDrafts("invalid queued shell command")
				m.push(m.text(MsgFollowupShellEmpty, MessageArgs{"glyph": glyphFailed(m.th)}))
				return nil
			}
			return m.beginSubmissionSource(submissionShell, command, draft, true)
		}
		if len(draft.Prefix) == 0 && len(draft.Paste) == 0 && strings.HasPrefix(text, "/") {
			if !safeQueuedSlash(text) {
				m.recallQueuedCommandForReview()
				return nil
			}
			queued, _ := m.followUps.popFront()
			m.recordHistory(queued)
			m.historyPos = len(m.history)
			cmd := m.slashCommand(text)
			m.layout()
			if cmd != nil {
				return cmd
			}
			if m.helpOpen || m.transcriptPager != nil || m.historySearch != nil ||
				m.checkpointPicker != nil || m.modelPicker != nil || m.sessionPicker != nil || m.keymapEditor != nil {
				return nil
			}
			continue
		}
		if m.call == nil {
			m.restoreQueuedDrafts("automatic submission failure")
			m.push(m.text(MsgFollowupDisconnected, MessageArgs{"glyph": glyphFailed(m.th)}))
			return nil
		}
		return m.beginSubmissionSource(submissionTask, "", draft, true)
	}
	return nil
}

func (m *Model) recallQueuedCommandForReview() {
	draft, ok := m.followUps.popFront()
	if !ok {
		return
	}
	if current := m.currentDraft(); !draftEmpty(current) {
		m.followUps.enqueue(current)
	}
	m.restoreDraft(draft)
	m.resetComposerUndo()
	m.queueRecallPending = true
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgFollowupSlashRecalled, nil)))
	m.layout()
}

func (m *Model) recallQueuedSubmissionForRetry() {
	draft, ok := m.followUps.popFront()
	if !ok {
		return
	}
	if current := m.currentDraft(); !draftEmpty(current) {
		m.followUps.enqueue(current)
	}
	m.restoreDraft(draft)
	m.resetComposerUndo()
	m.queueRecallPending = true
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgFollowupRetryRecalled, nil)))
	m.layout()
}

func taskStatusTerminal(status string) (terminal, successful bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return true, true
	case "failed", "cancelled", "canceled", "degraded", "aborted", "denied":
		return true, false
	default:
		return false, false
	}
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
	outcome, terminal := terminalConversationEvent(ev)
	return terminal, terminal && outcome == outcomeCompleted
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
		if id != "" && m.inFlightTaskID == "" && m.submitting != nil &&
			m.submitting.kind == submissionTask {
			if m.earlyTerminals == nil || len(m.earlyTerminals) >= 32 {
				m.earlyTerminals = make(map[string]earlyTaskTerminal)
			}
			m.earlyTerminals[id] = earlyTaskTerminal{
				generation: m.submitting.generation,
				successful: successful,
			}
		}
		return nil
	}
	m.inFlightTaskID = ""
	if !successful {
		m.restoreQueuedDrafts("task failure")
		return nil
	}
	return m.maybeSubmitNextQueued()
}
