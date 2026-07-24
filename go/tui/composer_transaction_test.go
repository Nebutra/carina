package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type blockingAckCaller struct {
	started chan map[string]any
	release chan struct{}
	result  any
	err     error
}

func (c *blockingAckCaller) Call(method string, params any, result any) error {
	if method != "task.submit" {
		return errors.New("unexpected RPC method: " + method)
	}
	raw, _ := json.Marshal(params)
	var captured map[string]any
	_ = json.Unmarshal(raw, &captured)
	c.started <- captured
	<-c.release
	if c.err != nil {
		return c.err
	}
	if result == nil || c.result == nil {
		return nil
	}
	raw, _ = json.Marshal(c.result)
	return json.Unmarshal(raw, result)
}

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
	if m.submitting != nil || !strings.Contains(m.statusActivityText(), "draft kept for retry") {
		t.Fatal("failure must leave an actionable retry state")
	}
	if strings.Contains(transcriptText(m), "draft kept for retry") {
		t.Fatal("submission recovery state must not enter permanent transcript")
	}

	fc.handler["task.submit"] = map[string]any{"task_id": "tsk_retry"}
	cmd, _ = m.handleKey("enter")
	drain(m, cmd)
	if len(fc.calls) != 2 || m.input.Value() != "" || len(m.pendingPaste) != 0 {
		t.Fatal("retry must send once more and clear only after success")
	}
}

func TestSubmissionAckPendingTypeAheadOwnsIndependentDraft(t *testing.T) {
	caller := &blockingAckCaller{
		started: make(chan map[string]any, 1),
		release: make(chan struct{}),
		result:  map[string]any{"task_id": "tsk_typeahead", "status": "running"},
	}
	m := New(Options{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(SessionReadyMsg{SessionID: "sess_typeahead", Call: caller})
	m.conversation.Readiness = readinessReady
	m.input.SetValue("ship frozen")
	m.pendingPaste = []string{"old\npaste"}

	cmd, handled := m.handleKey("enter")
	if !handled || cmd == nil || m.submitting == nil {
		t.Fatal("submission did not enter ACK-pending state")
	}
	frozen := cloneDraft(m.submitting.draft)
	clientID := m.submitting.clientID
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	params := <-caller.started

	for _, r := range "next" {
		m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
	}
	m.Update(tea.PasteMsg{Content: "new\npaste"})
	if !m.submitting.composerDetached {
		t.Fatal("first ordinary input did not detach a next draft")
	}
	if !draftsEqual(m.submitting.draft, frozen) || m.submitting.clientID != clientID {
		t.Fatalf("type-ahead mutated frozen submission: %#v", m.submitting)
	}
	if m.retrySubmission == nil || m.retrySubmission.clientID != clientID ||
		m.retrySubmission.prompt != "ship frozen\nold\npaste" ||
		!draftsEqual(m.retrySubmission.draft, frozen) {
		t.Fatalf("type-ahead mutated idempotency recovery snapshot: %#v", m.retrySubmission)
	}
	if got := m.currentDraft(); got.Text != "next" || len(got.Paste) != 1 || got.Paste[0] != "new\npaste" {
		t.Fatalf("next draft = %#v", got)
	}
	if params["prompt"] != "ship frozen\nold\npaste" || params["client_submission_id"] != clientID {
		t.Fatalf("wire snapshot drifted: %#v", params)
	}

	close(caller.release)
	msg := <-done
	m.Update(msg)
	if m.submitting != nil || m.retrySubmission != nil {
		t.Fatalf("successful ACK left pending state: submitting=%#v retry=%#v", m.submitting, m.retrySubmission)
	}
	if got := m.currentDraft(); got.Text != "next" || len(got.Paste) != 1 || got.Paste[0] != "new\npaste" {
		t.Fatalf("successful ACK consumed next draft: %#v", got)
	}
}

func TestSteerAndShellAcknowledgementsPreserveTypeAhead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		input  string
		setup  func(*Model)
		result any
	}{
		{
			name: "steer", method: "task.steer", input: "change direction",
			setup: func(m *Model) { m.inFlightTaskID = "tsk_active" },
		},
		{
			name: "shell", method: "command.exec", input: "!printf old",
			setup: func(*Model) {}, result: map[string]any{"ok": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{handler: map[string]any{tc.method: tc.result}}
			m, _ := newTestModel(fc)
			tc.setup(m)
			m.input.SetValue(tc.input)
			cmd, _ := m.handleKey("enter")
			if cmd == nil {
				t.Fatal("submission command is nil")
			}
			for _, r := range "next" {
				m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
			}
			drain(m, cmd)
			if got := m.currentDraft(); got.Text != "next" || len(got.Prefix) != 0 || len(got.Paste) != 0 {
				t.Fatalf("ACK consumed next draft: %#v", got)
			}
			if len(fc.calls) != 1 || fc.calls[0].method != tc.method {
				t.Fatalf("RPC calls = %#v", fc.calls)
			}
		})
	}
}

func TestSubmissionFailuresPrependFrozenDraftBeforeTypeAhead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		input  string
		setup  func(*Model)
	}{
		{name: "task", method: "task.submit", input: "old task", setup: func(*Model) {}},
		{
			name: "steer", method: "task.steer", input: "old steer",
			setup: func(m *Model) { m.inFlightTaskID = "tsk_active" },
		},
		{name: "shell", method: "command.exec", input: "!printf old", setup: func(*Model) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{handler: map[string]any{tc.method: errors.New("ack lost")}}
			m, _ := newTestModel(fc)
			tc.setup(m)
			m.input.SetValue(tc.input)
			cmd, _ := m.handleKey("enter")
			frozen := cloneDraft(m.submitting.draft)
			for _, r := range "next" {
				m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
			}
			m.Update(tea.PasteMsg{Content: "new\npaste"})
			drain(m, cmd)

			got := m.currentDraft()
			oldPrompt := draftPrompt(submissionOwnedDraft(&submissionState{
				draft: frozen, consumePaste: tc.method != "command.exec",
			}))
			if len(got.Prefix) != 1 || got.Prefix[0] != oldPrompt || got.Text != "next" ||
				len(got.Paste) != 1 || got.Paste[0] != "new\npaste" {
				t.Fatalf("failure merge order = %#v, old=%q", got, oldPrompt)
			}
			if tc.method == "task.submit" {
				if m.retrySubmission == nil || m.retrySubmission.prompt != oldPrompt ||
					!draftsEqual(m.retrySubmission.draft, frozen) {
					t.Fatalf("task idempotency snapshot degraded: %#v", m.retrySubmission)
				}
			}
			if len(m.composerUndo.undo) != 0 || len(m.composerUndo.redo) != 0 {
				t.Fatal("failure merge retained undo snapshots from the previous ownership epoch")
			}
		})
	}
}

func TestPendingSubmissionControlsAndOverlayKeepPriority(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_controls"},
	}}
	m, _ := newTestModel(fc)
	m.input.SetValue("frozen")
	cmd, _ := m.handleKey("enter")
	if cmd == nil || m.submitting == nil {
		t.Fatal("submission did not start")
	}
	if redraw, handled := m.handleKey("ctrl+l"); !handled || redraw == nil {
		t.Fatal("redraw was not immediate during ACK wait")
	}
	if help, handled := m.handleKey("f1"); !handled || help != nil || !m.helpOpen {
		t.Fatal("help was not immediate during ACK wait")
	}
	m.closeHelp()
	if interrupt, handled := m.handleKey("ctrl+c"); !handled || interrupt != nil {
		t.Fatal("first interrupt was not handled during ACK wait")
	}
	if m.submitting.composerDetached || m.input.Value() != "frozen" {
		t.Fatal("control keys detached or mutated the submitted draft")
	}

	m.approval = &approvalState{DecisionID: "perm_pending", Action: "command.exec"}
	m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	m.Update(tea.PasteMsg{Content: "hidden"})
	if m.submitting.composerDetached || m.input.Value() != "frozen" {
		t.Fatal("governance overlay leaked input into type-ahead")
	}
	m.approval = nil
	m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if !m.submitting.composerDetached || m.input.Value() != "x" {
		t.Fatal("ordinary key did not start the next draft after overlay closed")
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
	// Lone `!` enters sticky shell mode (Grok) instead of leaving a dead draft.
	m.input.SetValue("!")
	cmd, _ = m.handleKey("enter")
	if cmd != nil || !m.inShellMode() || m.input.Value() != "" {
		t.Fatalf("lone ! should enter sticky shell mode: cmd=%v mode=%v value=%q", cmd != nil, m.inShellMode(), m.input.Value())
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
	m.layout()
	box := m.componentFrame.Root.Bounds
	m.dispatchComponentPointer(tea.MouseWheelMsg{X: box.X, Y: box.Y, Button: tea.MouseWheelDown})
	if m.approval.Scroll == 0 {
		t.Fatal("mouse wheel must scroll the active approval body")
	}

	m.approval = nil
	m.question = &questionState{Prompt: strings.Repeat("question ", 400), Options: []questionOption{{Label: "yes"}}}
	m.layout()
	box = m.componentFrame.Root.Bounds
	m.dispatchComponentPointer(tea.MouseWheelMsg{X: box.X, Y: box.Y, Button: tea.MouseWheelDown})
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
	if !m.helpOpen {
		t.Fatal("/help must open the immediate help overlay")
	}
	m.closeHelp()

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
