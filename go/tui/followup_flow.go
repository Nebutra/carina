package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

func draftEmpty(draft promptDraft) bool {
	return strings.TrimSpace(draft.Text) == "" && len(draft.Prefix) == 0 && len(draft.Paste) == 0 && len(draft.Attachments) == 0
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
	m.setOperationalNotice(m.text(MsgFollowupQueued, MessageArgs{"count": m.followUps.len()}), theme.RoleMuted)
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
	m.setOperationalNotice(m.text(MsgFollowupRecalled, nil), theme.RoleMuted)
	m.layout()
	return true
}

func (m *Model) clearComposerDraft() {
	m.input.Reset()
	m.pendingPrefix = nil
	m.pendingPaste = nil
	m.attachments = nil
	m.clearAttachmentInteraction()
	m.resetComposerUndo()
	if m.suggest != nil {
		m.closeSuggest()
	}
	m.layout()
}

func mergeDraftsForRestore(drafts []promptDraft, current promptDraft) promptDraft {
	prefix := make([]string, 0, len(drafts)+len(current.Prefix))
	attachments := make([]draftAttachment, 0, len(current.Attachments))
	for _, draft := range drafts {
		prefix = append(prefix, draftPrompt(draft))
		detached := cloneAttachments(draft.Attachments)
		for i := range detached {
			// Restored queued prompts become detached prefix blocks, so their media
			// remains sendable but no longer claims adjacency to the live textarea.
			detached[i].TextOffset = -1
		}
		attachments = append(attachments, detached...)
	}
	prefix = append(prefix, current.Prefix...)
	attachments = append(attachments, cloneAttachments(current.Attachments)...)
	return promptDraft{
		Prefix:      prefix,
		Text:        current.Text,
		Paste:       append([]string(nil), current.Paste...),
		Attachments: attachments,
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
	m.setOperationalNotice(m.countText(MsgFollowupRestored, len(drafts), nil), theme.RoleMuted)
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
				m.setOperationalNotice(m.text(MsgFollowupShellEmpty, MessageArgs{"glyph": glyphFailed(m.th)}), theme.RoleError)
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
			m.setOperationalNotice(m.text(MsgFollowupDisconnected, MessageArgs{"glyph": glyphFailed(m.th)}), theme.RoleError)
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
	m.setOperationalNotice(m.text(MsgFollowupSlashRecalled, nil), theme.RoleMuted)
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
	if m.operationalNotice.Role != theme.RoleError {
		m.setOperationalNotice(m.text(MsgFollowupRetryRecalled, nil), theme.RoleMuted)
	}
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
