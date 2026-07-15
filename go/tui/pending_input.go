package tui

import tea "charm.land/bubbletea/v2"

// submissionHasIndependentComposer reports whether the visible composer no
// longer owns the snapshot being acknowledged. This is an explicit ownership
// bit; comparing draft text is unsafe because a next draft may be identical.
func submissionHasIndependentComposer(state *submissionState) bool {
	return state != nil && (state.composerDetached || state.fromQueue || state.background)
}

// keyStartsSubmissionTypeAhead limits the ownership handoff to input that can
// actually create content. Navigation and composer commands remain frozen
// until there is a next draft to edit.
func (m *Model) keyStartsSubmissionTypeAhead(msg tea.KeyPressMsg) bool {
	if msg.Key().Text != "" {
		return true
	}
	return m.keys.matches(KeyContextComposer, ActionComposerNewline, msg.String())
}

// beginSubmissionTypeAhead atomically detaches the acknowledged snapshot from
// the visible composer. Task and steer submissions own the whole draft; shell
// submissions own only their command text, so unrelated paste items remain in
// the new draft.
func (m *Model) beginSubmissionTypeAhead() {
	state := m.submitting
	if state == nil || submissionHasIndependentComposer(state) {
		return
	}
	state.composerDetached = true
	m.input.Reset()
	if state.consumePaste {
		m.pendingPrefix = nil
		m.pendingPaste = nil
	}
	m.historyPos = len(m.history)
	m.historyScratch = promptDraft{}
	m.resetComposerUndo()
	m.closeSuggest()
	m.layout()
}

func submissionOwnedDraft(state *submissionState) promptDraft {
	if state == nil {
		return promptDraft{}
	}
	owned := promptDraft{
		Prefix: append([]string(nil), state.draft.Prefix...),
		Text:   state.draft.Text,
	}
	if state.consumePaste {
		owned.Paste = append([]string(nil), state.draft.Paste...)
	}
	return owned
}

// restoreFailedSubmission prepends the submitted wire prompt to the next
// draft. Prefix is the ownership boundary: retrying after removing the next
// draft produces the exact same prompt/idempotency identity, while special
// slash/shell syntax is never accidentally re-executed as part of a merge.
func (m *Model) restoreFailedSubmission(state *submissionState) {
	// Queue and journal-background submissions already had explicit recovery
	// ownership before type-ahead: their existing queue/retry paths preserve the
	// foreground composer. Only an interactive ownership handoff needs merging.
	if state == nil || !state.composerDetached {
		return
	}

	current := m.currentDraft()
	prefix := make([]string, 0, 1+len(current.Prefix))
	if submitted := draftPrompt(submissionOwnedDraft(state)); submitted != "" {
		prefix = append(prefix, submitted)
	}
	prefix = append(prefix, current.Prefix...)
	m.restoreDraft(promptDraft{
		Prefix: prefix,
		Text:   current.Text,
		Paste:  append([]string(nil), current.Paste...),
	})
	m.historyPos = len(m.history)
	m.historyScratch = promptDraft{}
	// Undo snapshots from the next draft predate the ownership merge and could
	// otherwise erase the recovered submission with one Ctrl+Z.
	m.resetComposerUndo()
}
