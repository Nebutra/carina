package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestExternalEditorFreezesDraftAndAppliesReplacement(t *testing.T) {
	m, _ := newTestModel(nil)
	m.getenv = func(key string) string {
		if key == "EDITOR" {
			return "true"
		}
		return ""
	}
	m.input.SetValue("before")
	m.pendingPaste = []string{"paste payload"}
	cmd, handled := m.handleWorkspaceKey("ctrl+g")
	if !handled || cmd == nil || m.editor == nil {
		t.Fatalf("editor did not enter preparing state: handled=%v cmd=%v state=%#v", handled, cmd, m.editor)
	}
	state := m.editor
	info, err := os.Stat(state.draft.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("editor temp mode = %v err=%v", info, err)
	}

	m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	m.Update(tea.PasteMsg{Content: "hidden\ncontent"})
	if got := m.currentDraft(); got.Text != "before" || len(got.Paste) != 1 || got.Paste[0] != "paste payload" {
		t.Fatalf("editor preparation did not freeze draft: %#v", got)
	}
	if err := os.WriteFile(state.draft.path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Update(externalEditorDoneMsg{generation: state.generation})
	if m.editor != nil {
		t.Fatal("editor state remained active after completion")
	}
	if got := m.currentDraft(); got.Text != "after\n" || len(got.Paste) != 1 || got.Paste[0] != "paste payload" {
		t.Fatalf("editor replacement lost draft state: %#v", got)
	}
	if _, err := os.Stat(state.draft.path); !os.IsNotExist(err) {
		t.Fatalf("editor temp file remained: %v", err)
	}
	if m.View().Cursor == nil {
		t.Fatal("composer cursor was not restored after editor completion")
	}
}

func TestExternalEditorFailureRestoresExactDraft(t *testing.T) {
	m, _ := newTestModel(nil)
	m.getenv = func(string) string { return "false" }
	original := promptDraft{Text: "keep", Paste: []string{"one", "two"}}
	m.restoreDraft(original)
	m.input.SetCursorColumn(2)
	wantRow, wantCol := m.input.Line(), m.input.Column()
	cmd := m.beginExternalEditor(original)
	if cmd == nil || m.editor == nil {
		t.Fatal("editor did not start")
	}
	state := m.editor
	m.Update(externalEditorDoneMsg{generation: state.generation, err: errors.New("exit status 1")})
	if got := m.currentDraft(); !draftsEqual(got, original) {
		t.Fatalf("editor failure draft = %#v", got)
	}
	if m.input.Line() != wantRow || m.input.Column() != wantCol {
		t.Fatalf("editor failure caret = %d:%d, want %d:%d", m.input.Line(), m.input.Column(), wantRow, wantCol)
	}
	if _, err := os.Stat(state.draft.path); !os.IsNotExist(err) {
		t.Fatalf("failed editor temp file remained: %v", err)
	}
}

func TestExternalEditorSuccessIsUndoableReplacement(t *testing.T) {
	m, _ := newTestModel(nil)
	m.getenv = func(string) string { return "true" }
	composerType(t, m, "before")
	cmd := m.beginExternalEditor(m.currentDraft())
	if cmd == nil || m.editor == nil {
		t.Fatal("editor did not start")
	}
	session := m.editor
	if err := os.WriteFile(session.draft.path, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Update(externalEditorDoneMsg{generation: session.generation})
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "before" {
		t.Fatalf("undo after editor = %q", got)
	}
}

func TestTaskFailureDuringEditorDefersQueueRestoreUntilEditedDraftReturns(t *testing.T) {
	m, _ := newTestModel(nil)
	m.getenv = func(string) string { return "true" }
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "queued", Paste: []string{"queued paste"}})
	m.input.SetValue("editing")
	cmd := m.beginExternalEditor(m.currentDraft())
	if cmd == nil || m.editor == nil {
		t.Fatal("editor did not start")
	}
	session := m.editor
	m.Update(taskCompletedEvent("tsk_running", "failed"))
	if m.followUps.len() != 1 || m.input.Value() != "editing" || m.queueRestoreReason == "" {
		t.Fatalf("failure restore was not deferred: queue=%#v input=%q reason=%q", m.followUps.drafts, m.input.Value(), m.queueRestoreReason)
	}
	if err := os.WriteFile(session.draft.path, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Update(externalEditorDoneMsg{generation: session.generation})
	if m.followUps.len() != 0 || m.input.Value() != "edited" || draftPrompt(m.currentDraft()) != "queued\nqueued paste\nedited" {
		t.Fatalf("deferred restore lost queue/editor result: queue=%#v input=%q", m.followUps.drafts, m.input.Value())
	}
	if len(m.pendingPrefix) != 1 || m.pendingPrefix[0] != "queued\nqueued paste" {
		t.Fatalf("deferred restore lost detached segment: %#v", m.pendingPrefix)
	}
}

func TestTaskSuccessDuringEditorAutoDrainsAfterEditorCloses(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_followup"},
	}}
	m, _ := newTestModel(fc)
	m.getenv = func(string) string { return "true" }
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "queued"})
	cmd := m.beginExternalEditor(m.currentDraft())
	if cmd == nil || m.editor == nil {
		t.Fatal("editor did not start")
	}
	session := m.editor
	m.Update(taskCompletedEvent("tsk_running", "completed"))
	if m.followUps.len() != 1 || m.submitting != nil {
		t.Fatalf("queue drained while editor was active: queue=%#v submitting=%#v", m.followUps.drafts, m.submitting)
	}
	if err := os.WriteFile(session.draft.path, []byte("edited current"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cmd = m.Update(externalEditorDoneMsg{generation: session.generation})
	if cmd == nil {
		t.Fatal("editor close did not resume queued auto-submit")
	}
	drain(m, cmd)
	if m.followUps.len() != 0 || m.inFlightTaskID != "tsk_followup" || m.input.Value() != "edited current" {
		t.Fatalf("post-editor drain: queue=%#v task=%q input=%q", m.followUps.drafts, m.inFlightTaskID, m.input.Value())
	}
}

func TestCopyUsesLastRenderedAgentProjectionAndSurfacesErrors(t *testing.T) {
	m, _ := newTestModel(nil)
	m.tr.push("operator note")
	m.tr.pushPresentation(eventPresentation{
		Kind: presentationAgent, Status: statusSuccess, Title: "answer",
		Summary: "done", Body: []string{"copy this result"},
	}, theme.New(theme.ANSI256), 80)
	var copied string
	m.clipboardWrite = func(text string) error {
		copied = text
		return nil
	}
	cmd := m.copyLastAgentProjection()
	if cmd == nil {
		t.Fatal("copy did not schedule clipboard work")
	}
	m.Update(cmd())
	if !strings.Contains(copied, "copy this result") || strings.Contains(copied, "operator note") || strings.Contains(copied, "\x1b[") {
		t.Fatalf("copied projection = %q", copied)
	}

	m.clipboardWrite = func(string) error { return errors.New("clipboard unavailable") }
	m.Update(m.copyLastAgentProjection()())
	if !strings.Contains(transcriptText(m), "copy failed: clipboard unavailable") {
		t.Fatalf("copy failure not visible:\n%s", transcriptText(m))
	}
}

func TestCopyWithoutAgentProjectionIsActionable(t *testing.T) {
	m, _ := newTestModel(nil)
	if cmd := m.copyLastAgentProjection(); cmd != nil {
		t.Fatal("empty copy scheduled clipboard work")
	}
	if !strings.Contains(transcriptText(m), "nothing to copy") {
		t.Fatalf("empty copy error not visible:\n%s", transcriptText(m))
	}
}

func TestTranscriptPagerIsPlainScrollableAndRestoresCursor(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.ANSI256), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 18, Height: 6})
	for i := 0; i < 12; i++ {
		m.push(m.th.Style(theme.RoleInfo).Render("line with long text"))
	}
	if _, handled := m.handleKey("alt+r"); !handled || m.transcriptPager == nil {
		t.Fatal("Alt+R did not open transcript pager")
	}
	view := m.View()
	if view.Cursor != nil {
		t.Fatalf("transcript pager leaked composer cursor: %+v", view.Cursor.Position)
	}
	if strings.Contains(view.Content, "\x1b[") {
		t.Fatalf("copy-friendly transcript contains ANSI: %q", view.Content)
	}
	assertWorkspaceViewFits(t, view.Content, 18, 6)

	before := m.transcriptPager.scroll
	m.handleKey("pgdown")
	if m.transcriptPager.scroll <= before {
		t.Fatal("transcript pager did not scroll")
	}
	m.handleKey("esc")
	if m.transcriptPager != nil {
		t.Fatal("Esc did not close transcript pager")
	}
	if m.View().Cursor == nil {
		t.Fatal("composer cursor was not restored after pager close")
	}
}

func TestTranscriptPagerFitsOneCellTerminal(t *testing.T) {
	m, _ := newTestModel(nil)
	m.push("wide transcript content")
	m.openTranscriptPager()
	m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	view := m.View()
	assertWorkspaceViewFits(t, view.Content, 1, 1)
	if view.Cursor != nil {
		t.Fatal("one-cell pager leaked composer cursor")
	}
}

func TestWorkspaceSlashActionsAreWired(t *testing.T) {
	t.Run("transcript", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.push("plain transcript")
		m.input.SetValue("/transcript")
		cmd, handled := m.handleKey("enter")
		if !handled || cmd != nil || m.transcriptPager == nil {
			t.Fatalf("/transcript result: handled=%v cmd=%v pager=%#v", handled, cmd, m.transcriptPager)
		}
		if m.input.Value() != "" {
			t.Fatalf("/transcript command remained in composer: %q", m.input.Value())
		}
	})

	t.Run("copy", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.tr.pushPresentation(eventPresentation{
			Kind: presentationAgent, Status: statusSuccess, Title: "answer", Body: []string{"result"},
		}, theme.New(theme.Mono), 80)
		var copied string
		m.clipboardWrite = func(text string) error { copied = text; return nil }
		m.input.SetValue("/copy")
		cmd, handled := m.handleKey("enter")
		if !handled || cmd == nil {
			t.Fatalf("/copy result: handled=%v cmd=%v", handled, cmd)
		}
		m.Update(cmd())
		if !strings.Contains(copied, "result") || m.input.Value() != "" {
			t.Fatalf("/copy copied=%q composer=%q", copied, m.input.Value())
		}
	})

	t.Run("editor", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.getenv = func(string) string { return "true" }
		m.input.SetValue("/editor")
		m.pendingPaste = []string{"preserve"}
		cmd, handled := m.handleKey("enter")
		if !handled || cmd == nil || m.editor == nil {
			t.Fatalf("/editor result: handled=%v cmd=%v editor=%#v", handled, cmd, m.editor)
		}
		if m.editor.draft.original.Text != "" || len(m.editor.draft.original.Paste) != 1 {
			t.Fatalf("/editor exposed command text to editor: %#v", m.editor.draft.original)
		}
		state := m.editor
		m.Update(externalEditorDoneMsg{generation: state.generation, err: errors.New("cancelled")})
	})
}

func assertWorkspaceViewFits(t *testing.T, content string, width, height int) {
	t.Helper()
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		t.Fatalf("view height %d exceeds %d:\n%s", len(lines), height, content)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line %d width %d exceeds %d: %q", i, got, width, line)
		}
	}
}
