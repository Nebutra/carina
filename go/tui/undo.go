package tui

import (
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

const (
	composerUndoGroupWindow = 400 * time.Millisecond
	composerUndoLimit       = 200
)

type composerEditKind int

const (
	composerEditOther composerEditKind = iota
	composerEditTyping
	composerEditPaste
)

type composerSnapshot struct {
	draft                   promptDraft
	row                     int
	col                     int
	attachmentFocusID       string
	attachmentCaretAffinity attachmentCaretAffinity
}

type composerUndoEntry struct {
	before composerSnapshot
	after  composerSnapshot
	kind   composerEditKind
	at     time.Time
}

type composerUndoState struct {
	undo      []composerUndoEntry
	redo      []composerUndoEntry
	groupOpen bool
}

func (m *Model) composerSnapshot() composerSnapshot {
	focusID := ""
	if m.attachmentFocus >= 0 && m.attachmentFocus < len(m.attachments) {
		focusID = m.attachments[m.attachmentFocus].ID
	}
	return composerSnapshot{
		draft:                   m.currentDraft(),
		row:                     m.input.Line(),
		col:                     m.input.Column(),
		attachmentFocusID:       focusID,
		attachmentCaretAffinity: m.attachmentCaretAffinity,
	}
}

func (m *Model) recordComposerEdit(before composerSnapshot, kind composerEditKind) {
	previewChanged := m.reconcileInlineAttachments(before)
	after := m.composerSnapshot()
	// Caret-only navigation is a grouping boundary, not an undoable edit.
	// The caret still lives in snapshots so real edits restore its prior site.
	if draftsEqual(before.draft, after.draft) {
		if kind != composerEditTyping {
			m.breakComposerUndoGroup()
		}
		if previewChanged {
			m.layout()
		}
		return
	}

	now := m.now()
	state := &m.composerUndo
	state.redo = nil
	if kind == composerEditTyping && state.groupOpen && len(state.undo) > 0 {
		last := &state.undo[len(state.undo)-1]
		if last.kind == composerEditTyping && !now.Before(last.at) && now.Sub(last.at) <= composerUndoGroupWindow {
			last.after = after
			last.at = now
			if previewChanged {
				m.layout()
			}
			return
		}
	}

	state.undo = append(state.undo, composerUndoEntry{
		before: before,
		after:  after,
		kind:   kind,
		at:     now,
	})
	if len(state.undo) > composerUndoLimit {
		state.undo = append([]composerUndoEntry(nil), state.undo[len(state.undo)-composerUndoLimit:]...)
	}
	state.groupOpen = kind == composerEditTyping
	if previewChanged {
		m.layout()
	}
}

func (m *Model) breakComposerUndoGroup() {
	m.composerUndo.groupOpen = false
}

func (m *Model) composerExternalMutation() {
	m.breakComposerUndoGroup()
	m.composerUndo.redo = nil
}

func (m *Model) resetComposerUndo() {
	m.composerUndo = composerUndoState{}
}

func (m *Model) undoComposer() bool {
	state := &m.composerUndo
	if len(state.undo) == 0 {
		state.groupOpen = false
		return false
	}
	entry := state.undo[len(state.undo)-1]
	state.undo = state.undo[:len(state.undo)-1]
	state.redo = append(state.redo, entry)
	state.groupOpen = false
	m.restoreComposerSnapshot(entry.before)
	return true
}

func (m *Model) redoComposer() bool {
	state := &m.composerUndo
	if len(state.redo) == 0 {
		state.groupOpen = false
		return false
	}
	entry := state.redo[len(state.redo)-1]
	state.redo = state.redo[:len(state.redo)-1]
	state.undo = append(state.undo, entry)
	state.groupOpen = false
	m.restoreComposerSnapshot(entry.after)
	return true
}

func (m *Model) restoreComposerSnapshot(snapshot composerSnapshot) {
	m.input.SetValue(snapshot.draft.Text)
	m.input.MoveToBegin()
	// textarea exposes logical row movement rather than a direct row setter.
	// Walk visual rows with a strict content-derived guard until the requested
	// logical line is reached, then restore its rune column.
	guard := len([]rune(snapshot.draft.Text)) + m.input.LineCount() + 1
	for steps := 0; m.input.Line() < snapshot.row && steps < guard; steps++ {
		m.input.CursorDown()
	}
	m.input.SetCursorColumn(snapshot.col)
	m.pendingPrefix = append([]string(nil), snapshot.draft.Prefix...)
	m.pendingPaste = append([]string(nil), snapshot.draft.Paste...)
	m.attachments = cloneAttachments(snapshot.draft.Attachments)
	m.attachmentFocus = attachmentIndex(m.attachments, snapshot.attachmentFocusID)
	m.attachmentCaretAffinity = snapshot.attachmentCaretAffinity
	m.attachmentHoverID = ""
	m.syncAttachmentPreviewOwner()
	m.historyPos = len(m.history)
	m.historyScratch = promptDraft{}
	m.pasteBurst.reset()
	m.closeSuggest()
	m.layout()
}

// undoLatestPendingPaste preserves the established composer contract: while
// detached paste items exist, Ctrl+Z removes the newest one before touching
// textarea edit history. Rebase stored snapshots at the same index so a later
// text undo cannot resurrect the explicitly removed paste.
func (m *Model) undoLatestPendingPaste() bool {
	if len(m.pendingPaste) > 0 {
		index := len(m.pendingPaste) - 1
		m.pendingPaste = m.pendingPaste[:index]
		m.composerUndo.dropPasteAt(index)
		m.composerExternalMutation()
		m.layout()
		return true
	}
	if len(m.pendingPrefix) == 0 {
		return false
	}
	index := len(m.pendingPrefix) - 1
	m.pendingPrefix = m.pendingPrefix[:index]
	m.composerUndo.dropPrefixAt(index)
	m.composerExternalMutation()
	m.layout()
	return true
}

func (s *composerUndoState) dropPrefixAt(index int) {
	remove := func(snapshot *composerSnapshot) {
		if index < 0 || index >= len(snapshot.draft.Prefix) {
			return
		}
		prefix := append([]string(nil), snapshot.draft.Prefix...)
		snapshot.draft.Prefix = append(prefix[:index], prefix[index+1:]...)
	}
	for i := range s.undo {
		remove(&s.undo[i].before)
		remove(&s.undo[i].after)
	}
	for i := range s.redo {
		remove(&s.redo[i].before)
		remove(&s.redo[i].after)
	}
}

func (s *composerUndoState) dropPasteAt(index int) {
	remove := func(snapshot *composerSnapshot) {
		if index < 0 || index >= len(snapshot.draft.Paste) {
			return
		}
		paste := append([]string(nil), snapshot.draft.Paste...)
		snapshot.draft.Paste = append(paste[:index], paste[index+1:]...)
	}
	for i := range s.undo {
		remove(&s.undo[i].before)
		remove(&s.undo[i].after)
	}
	for i := range s.redo {
		remove(&s.redo[i].before)
		remove(&s.redo[i].after)
	}
}

func composerKeyEditKind(msg tea.KeyPressMsg) composerEditKind {
	key := msg.Key()
	if key.Text == "" || key.Mod.Contains(tea.ModCtrl) || key.Mod.Contains(tea.ModAlt) ||
		key.Mod.Contains(tea.ModMeta) || key.Mod.Contains(tea.ModHyper) || key.Mod.Contains(tea.ModSuper) {
		return composerEditOther
	}
	runes := []rune(key.Text)
	if len(runes) > 1 {
		for _, r := range runes {
			if r == '\u200d' {
				// A terminal that commits the whole ZWJ grapheme in one event has
				// already supplied an atomic edit. Split-event ZWJ sequences still
				// flow through the typing group below.
				return composerEditOther
			}
		}
	}
	for _, r := range runes {
		// Format runes such as the emoji ZWJ are not unicode.IsPrint, but they
		// are part of the same grapheme the operator is composing. Only actual
		// control characters split the typing transaction.
		if unicode.IsControl(r) || r == '\n' || r == '\r' || r == '\t' {
			return composerEditOther
		}
	}
	return composerEditTyping
}
