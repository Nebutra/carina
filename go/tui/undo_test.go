package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestComposerUndoGroupsRapidTypingAndRedoRestoresIt(t *testing.T) {
	m, clock := newTestModel(nil)
	for _, r := range "hello" {
		composerType(t, m, string(r))
		clock.advance(80 * time.Millisecond)
	}
	if len(m.composerUndo.undo) != 1 {
		t.Fatalf("rapid typing transactions = %d, want 1", len(m.composerUndo.undo))
	}

	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("rapid typing undo = %q, want empty", got)
	}
	composerKey(t, m, "ctrl+shift+z")
	if got := m.input.Value(); got != "hello" {
		t.Fatalf("rapid typing redo = %q, want hello", got)
	}
}

func TestComposerUndoTypingPauseCreatesTwoTransactions(t *testing.T) {
	m, clock := newTestModel(nil)
	composerType(t, m, "a")
	clock.advance(composerUndoGroupWindow + time.Millisecond)
	composerType(t, m, "b")
	if len(m.composerUndo.undo) != 2 {
		t.Fatalf("paused typing transactions = %d, want 2", len(m.composerUndo.undo))
	}

	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "a" {
		t.Fatalf("first paused undo = %q, want a", got)
	}
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("second paused undo = %q, want empty", got)
	}
}

func TestComposerUndoCJKAndEmojiUseWholeSnapshots(t *testing.T) {
	m, clock := newTestModel(nil)
	composerType(t, m, "你")
	clock.advance(50 * time.Millisecond)
	composerType(t, m, "好")
	clock.advance(50 * time.Millisecond)
	// Send the ZWJ sequence as one terminal text event. It is one atomic
	// snapshot even though its UTF-8 and rune representation are multi-unit.
	composerType(t, m, "👨‍💻")
	if got := m.input.Value(); got != "你好👨‍💻" {
		t.Fatalf("unicode input = %q", got)
	}

	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "你好" {
		t.Fatalf("emoji undo split or corrupted grapheme: %q", got)
	}
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("CJK undo split grouped input: %q", got)
	}
}

func TestComposerUndoGroupsSplitZWJGrapheme(t *testing.T) {
	m, clock := newTestModel(nil)
	composerType(t, m, "👨")
	clock.advance(20 * time.Millisecond)
	composerType(t, m, "\u200d")
	clock.advance(20 * time.Millisecond)
	composerType(t, m, "💻")
	if got := m.input.Value(); got != "👨‍💻" {
		t.Fatalf("split grapheme input = %q", got)
	}
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("split grapheme undo left partial sequence: %q", got)
	}
}

func TestComposerUndoPendingPastePriorityDoesNotResurrectPaste(t *testing.T) {
	m, clock := newTestModel(nil)
	composerType(t, m, "A")
	clock.advance(10 * time.Millisecond)
	m.Update(tea.PasteMsg{Content: "first\nsecond"})
	composerType(t, m, "B")
	if got := m.currentDraft(); got.Text != "AB" || len(got.Paste) != 1 {
		t.Fatalf("precondition draft = %+v", got)
	}

	// Pending paste keeps its established priority even though B is the most
	// recent textarea edit.
	composerKey(t, m, "ctrl+z")
	if got := m.currentDraft(); got.Text != "AB" || len(got.Paste) != 0 {
		t.Fatalf("paste-priority undo = %+v", got)
	}
	composerKey(t, m, "ctrl+z")
	if got := m.currentDraft(); got.Text != "A" || len(got.Paste) != 0 {
		t.Fatalf("text undo resurrected removed paste: %+v", got)
	}
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("oldest text undo = %q", got)
	}
}

func TestComposerUndoSingleLinePasteIsAtomic(t *testing.T) {
	m, _ := newTestModel(nil)
	composerType(t, m, "A")
	m.Update(tea.PasteMsg{Content: "bulk"})
	if got := m.input.Value(); got != "Abulk" {
		t.Fatalf("single-line paste = %q", got)
	}
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "A" {
		t.Fatalf("atomic paste undo = %q", got)
	}
}

func TestComposerUndoNavigationCutsGroupAndRestoresCaret(t *testing.T) {
	m, clock := newTestModel(nil)
	composerType(t, m, "a")
	clock.advance(50 * time.Millisecond)
	composerType(t, m, "b")
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	composerType(t, m, "X")
	if got := m.input.Value(); got != "aXb" {
		t.Fatalf("middle edit = %q", got)
	}

	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "ab" || m.input.Line() != 0 || m.input.Column() != 1 {
		t.Fatalf("navigation undo text/caret = %q @ %d:%d", got, m.input.Line(), m.input.Column())
	}
	composerType(t, m, "Y")
	if got := m.input.Value(); got != "aYb" {
		t.Fatalf("restored caret was not editable in place: %q", got)
	}
}

func TestComposerUndoHistoryRestoreIsAGroupingBoundary(t *testing.T) {
	m, clock := newTestModel(nil)
	composerType(t, m, "draft")
	m.recordHistory(promptDraft{Text: "older"})
	m.historyPos = len(m.history)
	composerKey(t, m, "ctrl+p")
	if got := m.input.Value(); got != "older" {
		t.Fatalf("history recall = %q", got)
	}
	clock.advance(10 * time.Millisecond)
	composerType(t, m, "X")
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "older" {
		t.Fatalf("post-history edit merged across boundary: %q", got)
	}
}

func TestComposerUndoModalOpeningCutsTypingGroup(t *testing.T) {
	m, clock := newTestModel(nil)
	composerType(t, m, "a")
	m.Update(permissionRequestEvent("undo_modal"))
	if m.approval == nil {
		t.Fatal("approval did not open")
	}
	m.approval = nil
	clock.advance(10 * time.Millisecond)
	composerType(t, m, "b")
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "a" {
		t.Fatalf("typing merged across modal boundary: %q", got)
	}
}

func TestComposerUndoSubmissionFailureKeepsStackAndSubmittingFreezesIt(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("offline")}}
	m, _ := newTestModel(fc)
	composerType(t, m, "retry me")
	beforeEntries := len(m.composerUndo.undo)

	cmd, handled := m.handleKey("enter")
	if !handled || cmd == nil || m.submitting == nil {
		t.Fatal("submission did not enter pending state")
	}
	composerKey(t, m, "ctrl+z")
	if m.input.Value() != "retry me" || len(m.composerUndo.undo) != beforeEntries {
		t.Fatal("Ctrl+Z mutated composer while submission was pending")
	}
	drain(m, cmd)
	if m.submitting != nil || m.input.Value() != "retry me" {
		t.Fatal("failed submission did not keep draft")
	}
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("failed submission discarded undo stack: %q", got)
	}
}

func TestComposerUndoSuccessfulSubmissionDoesNotResurrectSentDraft(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "undo_success"},
	}}
	m, _ := newTestModel(fc)
	composerType(t, m, "send once")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("undo resurrected submitted draft: %q", got)
	}
}

func TestComposerUndoDoesNotInterceptHistorySearch(t *testing.T) {
	m, _ := newTestModel(nil)
	composerType(t, m, "draft")
	m.history = []promptDraft{{Text: "history result"}}
	m.historyPos = len(m.history)
	startHistorySearch(t, m)
	typeHistoryQuery(t, m, "history")
	beforeEntries := len(m.composerUndo.undo)
	composerKey(t, m, "ctrl+z")
	if m.historySearch == nil || len(m.composerUndo.undo) != beforeEntries {
		t.Fatal("composer undo intercepted history-search input")
	}
	composerKey(t, m, "esc")
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("undo stack changed across history search: %q", got)
	}
}

func TestComposerUndoCapturesPasteBurstStructuralText(t *testing.T) {
	m, clock := newTestModel(nil)
	composerType(t, m, "a")
	clock.advance(time.Millisecond)
	composerType(t, m, "b")
	clock.advance(time.Millisecond)
	composerType(t, m, "c")
	clock.advance(time.Millisecond)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.input.Value(); got != "abc\n" {
		t.Fatalf("paste burst structural text = %q", got)
	}
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste burst undo = %q", got)
	}
}

func composerType(t *testing.T, m *Model, text string) {
	t.Helper()
	m.Update(tea.KeyPressMsg{Text: text})
}

func composerKey(t *testing.T, m *Model, key string) {
	t.Helper()
	cmd, handled := m.handleKey(key)
	if !handled || cmd != nil {
		t.Fatalf("composer key %q not handled locally", key)
	}
}
