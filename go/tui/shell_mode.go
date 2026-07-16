package tui

import (
	"strings"
)

// composerMode is the sticky input mode for the main prompt (Grok-style).
// Normal is chat/task; shell is governed argv commands without an agent turn.
type composerMode int

const (
	composerModeNormal composerMode = iota
	composerModeShell
)

func (m *Model) inShellMode() bool { return m.composerMode == composerModeShell }

func (m *Model) enterShellMode() {
	if m.composerMode == composerModeShell {
		return
	}
	m.composerMode = composerModeShell
	m.closeSuggest()
	m.applyComposerChrome()
	m.layout()
}

func (m *Model) exitShellMode() {
	if m.composerMode != composerModeShell {
		return
	}
	m.composerMode = composerModeNormal
	m.applyComposerChrome()
	m.layout()
}

// applyComposerChrome updates prompt/placeholder for the active composer mode.
func (m *Model) applyComposerChrome() {
	w := m.width
	if w < 1 {
		w = 1
	}
	if w < 4 {
		m.input.Prompt = ""
	} else if m.inShellMode() {
		m.input.Prompt = "! "
	} else {
		m.input.Prompt = "> "
	}
	if m.inShellMode() {
		m.input.Placeholder = m.text(MsgPlaceholderShell, nil)
	} else {
		m.input.Placeholder = m.text(MsgPlaceholderInstruction, MessageArgs{
			"submit":  primaryKeyLabel(m.keys.keys(KeyContextComposer, ActionComposerSubmit)),
			"newline": primaryKeyLabel(m.keys.keys(KeyContextComposer, ActionComposerNewline)),
			"help":    primaryKeyLabel(m.keys.keys(KeyContextGlobal, ActionGlobalHelp)),
		})
	}
}

// tryEnterShellModeFromKey handles Grok semantics: `!` on an empty normal
// prompt enters sticky shell mode without inserting the character.
func (m *Model) tryEnterShellModeFromKey(key string, text string) bool {
	if m.inShellMode() || !m.composerSurfaceIdle() {
		return false
	}
	if !draftEmpty(m.currentDraft()) {
		return false
	}
	trigger := key == "!" || text == "!"
	if !trigger {
		return false
	}
	m.enterShellMode()
	return true
}

// tryExitShellModeFromKey: Esc on empty shell prompt returns to normal
// (before rewind). Non-empty Esc is handled by clear/interrupt elsewhere.
func (m *Model) tryExitShellModeFromKey(key string) bool {
	if !m.inShellMode() {
		return false
	}
	if !m.keys.matches(KeyContextChat, ActionChatRewind, key) &&
		!m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) &&
		key != "esc" {
		return false
	}
	// Only exit on empty draft; non-empty Esc/ctrl+c keep existing clear semantics.
	if !draftEmpty(m.currentDraft()) {
		return false
	}
	// Prefer exit-mode over rewind when shell mode is sticky.
	if m.keys.matches(KeyContextChat, ActionChatRewind, key) || key == "esc" {
		m.exitShellMode()
		m.rewindPrimed = false
		return true
	}
	return false
}

func (m *Model) composerSurfaceIdle() bool {
	return m.approval == nil && m.question == nil && m.historySearch == nil &&
		!m.helpOpen && m.settings == nil && m.transcriptPager == nil &&
		m.checkpointPicker == nil && m.modelPicker == nil && m.sessionPicker == nil &&
		m.keymapEditor == nil && m.editor == nil
}

// shellCommandFromDraft extracts the command body for shell submission.
// Sticky mode does not require a leading !; one-shot !cmd in normal mode still works.
func shellCommandFromDraft(text string, sticky bool) (command string, ok bool) {
	text = strings.TrimSpace(text)
	if sticky {
		if strings.HasPrefix(text, "!") {
			text = strings.TrimSpace(strings.TrimPrefix(text, "!"))
		}
		return text, text != ""
	}
	if !strings.HasPrefix(text, "!") {
		return "", false
	}
	command = strings.TrimSpace(strings.TrimPrefix(text, "!"))
	return command, command != ""
}

// historyTextForShell stores a leading ! so history recall re-enters shell mode.
func historyTextForShell(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return "!"
	}
	if strings.HasPrefix(command, "!") {
		return command
	}
	return "!" + command
}

// normalizeDraftForShellHistory rewrites a shell submission draft for history.
func normalizeDraftForShellHistory(draft promptDraft, command string) promptDraft {
	out := cloneDraft(draft)
	out.Text = historyTextForShell(command)
	return out
}

// restoreDraftMode applies sticky shell mode when a history entry was a shell command.
func (m *Model) restoreDraftMode(draft promptDraft) promptDraft {
	text := strings.TrimSpace(draft.Text)
	if strings.HasPrefix(text, "!") && len(draft.Prefix) == 0 {
		m.enterShellMode()
		draft.Text = strings.TrimSpace(strings.TrimPrefix(text, "!"))
		return draft
	}
	// Non-shell history returns to normal mode so mode matches content.
	if m.inShellMode() {
		m.exitShellMode()
	}
	return draft
}

// absorbLoneBangIfNeeded converts a lone typed "!" in normal mode into sticky shell mode
// (fallback when the terminal delivered ! only after input.Update).
func (m *Model) absorbLoneBangIfNeeded() {
	if m.inShellMode() || !m.composerSurfaceIdle() {
		return
	}
	if len(m.pendingPrefix) > 0 || len(m.pendingPaste) > 0 {
		return
	}
	if m.input.Value() != "!" {
		return
	}
	m.input.Reset()
	m.enterShellMode()
}

// shellModeStatusSuffix is a short footer token when sticky shell is active.
func (m *Model) shellModeStatusSuffix() string {
	if !m.inShellMode() {
		return ""
	}
	return m.text(MsgStatusShellMode, nil)
}