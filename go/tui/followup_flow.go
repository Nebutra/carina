package tui

import (
	"fmt"
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
	m.resetComposerUndo()
	m.push(m.th.Style(theme.RoleMuted).Render("- recalled latest follow-up for editing"))
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
	if m.editor != nil || m.historySearch != nil {
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
	m.push(m.th.Style(theme.RoleMuted).Render(fmt.Sprintf("- restored %d queued follow-up(s) after %s", len(drafts), reason)))
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
		m.historySearch != nil || m.transcriptPager != nil || m.queueRecallPending || m.retrySubmission != nil {
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
				m.push(fmt.Sprintf("%s queued shell command is empty; drafts restored", glyphFailed(m.th)))
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
			_ = m.slashCommand(text) // safeQueuedSlash commands are synchronous.
			m.layout()
			if m.helpOpen || m.transcriptPager != nil || m.historySearch != nil {
				return nil
			}
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
	m.push(m.th.Style(theme.RoleMuted).Render("- queued slash command recalled; review and run it from the composer"))
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
	m.push(m.th.Style(theme.RoleMuted).Render("- unacknowledged queued submission recalled; Enter retries idempotently"))
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
	typ := strings.ToLower(str(ev["type"]))
	payload, _ := ev["payload"].(map[string]any)
	status := strings.ToLower(firstValue(ev, "status", "outcome"))
	if status == "" {
		status = strings.ToLower(firstValue(payload, "status", "outcome"))
	}
	failure := strings.Contains(status, "fail") || strings.Contains(status, "cancel") || strings.Contains(status, "degrad") ||
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
