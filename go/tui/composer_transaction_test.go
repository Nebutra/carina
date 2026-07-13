package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSubmissionKeepsDraftUntilAcknowledgedAndDeduplicatesEnter(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_tx"},
	}}
	m, _ := newTestModel(fc)
	m.input.SetValue("ship this")
	m.pendingPaste = []string{"alpha\nbeta"}

	first, handled := m.handleKey("enter")
	if !handled || first == nil {
		t.Fatal("first enter must start submission")
	}
	if got := m.input.Value(); got != "ship this" {
		t.Fatalf("draft cleared before acknowledgement: %q", got)
	}
	if len(m.pendingPaste) != 1 || m.submitting == nil {
		t.Fatal("paste and submitting state must remain while RPC is pending")
	}
	second, handled := m.handleKey("enter")
	if !handled || second != nil {
		t.Fatal("repeated enter must be consumed without issuing another command")
	}
	if _, handled := m.handleKey("ctrl+z"); !handled || len(m.pendingPaste) != 1 {
		t.Fatal("pending submission must freeze paste undo with the rest of the draft")
	}

	drain(m, first)
	if len(fc.calls) != 1 {
		t.Fatalf("task.submit calls = %d, want 1", len(fc.calls))
	}
	if got := fc.last().params["prompt"]; got != "ship this\nalpha\nbeta" {
		t.Fatalf("submitted prompt = %#v", got)
	}
	if m.input.Value() != "" || len(m.pendingPaste) != 0 || m.submitting != nil {
		t.Fatal("successful acknowledgement must commit and clear the draft")
	}
}

func TestSubmissionFailureKeepsExactDraftForRetry(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("offline")}}
	m, _ := newTestModel(fc)
	m.input.SetValue("  retry me  ")
	m.pendingPaste = []string{"one\ntwo"}

	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	if got := m.input.Value(); got != "  retry me  " {
		t.Fatalf("failed submit changed text: %q", got)
	}
	if len(m.pendingPaste) != 1 || m.pendingPaste[0] != "one\ntwo" {
		t.Fatalf("failed submit changed paste: %#v", m.pendingPaste)
	}
	if m.submitting != nil || !strings.Contains(transcriptText(m), "draft kept for retry") {
		t.Fatal("failure must leave an actionable retry state")
	}

	fc.handler["task.submit"] = map[string]any{"task_id": "tsk_retry"}
	cmd, _ = m.handleKey("enter")
	drain(m, cmd)
	if len(fc.calls) != 2 || m.input.Value() != "" || len(m.pendingPaste) != 0 {
		t.Fatal("retry must send once more and clear only after success")
	}
}

func TestDisconnectedSubmitAndInvalidCommandsPreserveDraft(t *testing.T) {
	m, _ := newTestModel(nil)
	m.input.SetValue("not connected")
	cmd, _ := m.handleKey("enter")
	if cmd != nil || m.input.Value() != "not connected" {
		t.Fatal("disconnected submit must keep the draft without launching a command")
	}

	m.input.SetValue("/search")
	cmd, _ = m.handleKey("enter")
	if cmd != nil || m.input.Value() != "/search" {
		t.Fatal("invalid local slash command must remain editable")
	}
	m.input.SetValue("!")
	cmd, _ = m.handleKey("enter")
	if cmd != nil || m.input.Value() != "!" {
		t.Fatal("invalid shell command must remain editable")
	}
}

func TestSteerAndShellFailuresPreserveDraft(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.steer":   errors.New("steer rejected"),
		"command.exec": errors.New("shell rejected"),
	}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_active"
	m.input.SetValue("change direction")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	if m.input.Value() != "change direction" {
		t.Fatal("failed steering must preserve its draft")
	}

	m.inFlightTaskID = ""
	m.input.SetValue("!printf hello")
	cmd, _ = m.handleKey("enter")
	drain(m, cmd)
	if m.input.Value() != "!printf hello" {
		t.Fatal("failed shell command must preserve its draft")
	}
}

func TestPastePreviewUndoAndHistoryRestoreWholeDraft(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_history"},
	}}
	m, _ := newTestModel(fc)
	m.Update(tea.PasteMsg{Content: "first\nsecond"})
	m.Update(tea.PasteMsg{Content: "third\nfourth"})
	view := m.View().Content
	for _, want := range []string{"pasted draft items", "first", "third", "ctrl+z"} {
		if !strings.Contains(view, want) {
			t.Fatalf("paste preview missing %q:\n%s", want, view)
		}
	}
	if _, handled := m.handleKey("ctrl+z"); !handled || len(m.pendingPaste) != 1 {
		t.Fatal("ctrl+z must remove exactly the latest paste item")
	}

	m.input.SetValue("with paste")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	m.input.SetValue("scratch")
	if _, handled := m.handleKey("ctrl+p"); !handled {
		t.Fatal("ctrl+p must recall prompt history")
	}
	if m.input.Value() != "with paste" || len(m.pendingPaste) != 1 || m.pendingPaste[0] != "first\nsecond" {
		t.Fatalf("history did not restore the whole draft: text=%q paste=%#v", m.input.Value(), m.pendingPaste)
	}
	if _, handled := m.handleKey("ctrl+n"); !handled {
		t.Fatal("ctrl+n must move back to the scratch draft")
	}
	if m.input.Value() != "scratch" || len(m.pendingPaste) != 0 {
		t.Fatalf("history did not restore scratch: text=%q paste=%#v", m.input.Value(), m.pendingPaste)
	}
}

func TestMultilineArrowNavigationPrecedesHistory(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.recordHistory(promptDraft{Text: "older"})
	m.historyPos = len(m.history)
	m.input.SetValue("first\nsecond")
	if _, handled := m.handleKey("up"); handled {
		t.Fatal("up inside a multiline textarea must move the caret before entering history")
	}
}

func TestPastePreviewSanitizesTerminalControls(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.pendingPaste = []string{"\x1b[31mred\x1b[0m\nnext"}
	view := m.View().Content
	if strings.Contains(view, "\x1b[31m") {
		t.Fatal("paste preview must not render payload control sequences")
	}
}

func TestMouseWheelRoutesToActiveScrollContext(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.approval = &approvalState{Body: make([]string, 40)}
	m.handleMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.approval.Scroll == 0 {
		t.Fatal("mouse wheel must scroll the active approval body")
	}

	m.approval = nil
	m.question = &questionState{Prompt: strings.Repeat("question ", 400), Options: []questionOption{{Label: "yes"}}}
	m.handleMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.question.Scroll == 0 {
		t.Fatal("mouse wheel must scroll the active question body")
	}
}

func TestSlashAndShellCommandsDoNotConsumePendingPaste(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"command.exec": map[string]any{"ok": true}}}
	m, _ := newTestModel(fc)
	m.pendingPaste = []string{"keep\nthis"}
	m.input.SetValue("/help")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	if m.input.Value() != "" || len(m.pendingPaste) != 1 {
		t.Fatalf("slash command consumed unrelated paste: text=%q paste=%#v", m.input.Value(), m.pendingPaste)
	}

	m.input.SetValue("!printf ok")
	cmd, _ = m.handleKey("enter")
	drain(m, cmd)
	if m.input.Value() != "" || len(m.pendingPaste) != 1 || m.pendingPaste[0] != "keep\nthis" {
		t.Fatalf("shell command consumed unrelated paste: text=%q paste=%#v", m.input.Value(), m.pendingPaste)
	}
	if _, hasPrompt := fc.last().params["prompt"]; hasPrompt {
		t.Fatal("shell RPC must not silently attach pending paste as a prompt")
	}
}

func TestPurePasteSubmissionPreservesSignificantWhitespace(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": map[string]any{"task_id": "tsk_ws"}}}
	m, _ := newTestModel(fc)
	payload := "  indented: true\n    child: value\n\n"
	m.pendingPaste = []string{payload}
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	if got := fc.last().params["prompt"]; got != payload {
		t.Fatalf("pure paste whitespace changed: got=%q want=%q", got, payload)
	}
}
