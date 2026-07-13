package tui

import (
	"errors"
	"strings"
	"testing"
)

func TestRunningEnterSteersImmediatelyWhileFollowUpsRemainQueued(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.steer": nil}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "next turn"})
	m.input.SetValue("steer now")

	cmd, handled := m.handleKey("enter")
	if !handled || cmd == nil {
		t.Fatal("running Enter did not start an immediate steer")
	}
	drain(m, cmd)
	if got := fc.last(); got.method != "task.steer" || got.params["message"] != "steer now" {
		t.Fatalf("running Enter call = %#v", got)
	}
	if m.followUps.len() != 1 {
		t.Fatalf("steer consumed follow-up queue: %#v", m.followUps.drafts)
	}
}

func TestTabQueuesFullDraftFIFOAndPreviewIsBounded(t *testing.T) {
	m, _ := newTestModel(nil)
	m.inFlightTaskID = "tsk_running"
	for i, text := range []string{"first", "second", "third", "fourth"} {
		m.input.SetValue(text)
		m.pendingPaste = []string{text + " paste"}
		if _, handled := m.handleKey("tab"); !handled {
			t.Fatalf("Tab %d was not handled", i)
		}
	}
	if m.followUps.len() != 4 {
		t.Fatalf("queued drafts = %#v", m.followUps.drafts)
	}
	for i, want := range []string{"first", "second", "third", "fourth"} {
		got := m.followUps.drafts[i]
		if got.Text != want || len(got.Paste) != 1 || got.Paste[0] != want+" paste" {
			t.Fatalf("queue[%d] = %#v", i, got)
		}
	}
	if !draftEmpty(m.currentDraft()) {
		t.Fatalf("queued composer was not cleared: %#v", m.currentDraft())
	}
	lines := m.queuePanelLines()
	if len(lines) != 4 || !strings.Contains(lines[0], "queued follow-ups: 4") {
		t.Fatalf("queue preview should be header + 3 drafts: %#v", lines)
	}
	if strings.Contains(strings.Join(lines, "\n"), "fourth") {
		t.Fatalf("queue preview exceeded three drafts: %#v", lines)
	}
}

func TestAltUpRecallsLastWithoutDroppingCurrentComposer(t *testing.T) {
	m, _ := newTestModel(nil)
	m.followUps.enqueue(promptDraft{Text: "first"})
	m.followUps.enqueue(promptDraft{Text: "edit me", Paste: []string{"queued paste"}})
	m.input.SetValue("current work")
	m.pendingPaste = []string{"current paste"}

	if _, handled := m.handleKey("alt+up"); !handled {
		t.Fatal("Alt+Up did not recall a follow-up")
	}
	if got := m.currentDraft(); got.Text != "edit me" || len(got.Paste) != 1 || got.Paste[0] != "queued paste" {
		t.Fatalf("recalled draft = %#v", got)
	}
	if m.followUps.len() != 2 || m.followUps.drafts[0].Text != "first" || m.followUps.drafts[1].Text != "current work" {
		t.Fatalf("current composer was not swapped into queue: %#v", m.followUps.drafts)
	}
	if got := m.followUps.drafts[1].Paste; len(got) != 1 || got[0] != "current paste" {
		t.Fatalf("current paste was dropped during recall: %#v", got)
	}
}

func TestCompletedTasksAutoDrainFollowUpsFIFO(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_followup"},
	}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_initial"
	m.followUps.enqueue(promptDraft{Text: "first"})
	m.followUps.enqueue(promptDraft{Text: "second", Paste: []string{"payload"}})

	_, cmd := m.Update(taskCompletedEvent("tsk_initial", "completed"))
	if cmd == nil || m.followUps.len() != 2 {
		t.Fatalf("first follow-up was removed before RPC success: cmd=%v queue=%#v", cmd, m.followUps.drafts)
	}
	drain(m, cmd)
	if m.followUps.len() != 1 || m.followUps.drafts[0].Text != "second" {
		t.Fatalf("first success did not pop exactly one FIFO item: %#v", m.followUps.drafts)
	}
	if got := fc.calls[0].params["prompt"]; got != "first" {
		t.Fatalf("first auto-submit prompt = %#v", got)
	}

	_, cmd = m.Update(taskCompletedEvent("tsk_followup", "completed"))
	if cmd == nil {
		t.Fatal("second completion did not schedule the next follow-up")
	}
	drain(m, cmd)
	if m.followUps.len() != 0 || len(fc.calls) != 2 {
		t.Fatalf("queue/calls after second drain: queue=%#v calls=%#v", m.followUps.drafts, fc.calls)
	}
	if got := fc.calls[1].params["prompt"]; got != "second\npayload" {
		t.Fatalf("second auto-submit prompt = %#v", got)
	}
}

func TestCancellationRestoresQueuedDraftsBeforeCurrentComposer(t *testing.T) {
	m, _ := newTestModel(nil)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "first", Paste: []string{"p1"}})
	m.followUps.enqueue(promptDraft{Text: "second", Paste: []string{"p2"}})
	m.input.SetValue("current")
	m.pendingPaste = []string{"p3"}

	m.Update(cancelDoneMsg{taskID: "tsk_running"})
	assertRestoredQueue(t, m)
}

func TestStaleCancellationDoesNotRestoreQueueForNewActiveTask(t *testing.T) {
	m, _ := newTestModel(nil)
	m.inFlightTaskID = "tsk_new"
	m.followUps.enqueue(promptDraft{Text: "queued"})
	m.Update(cancelDoneMsg{taskID: "tsk_old"})
	if m.inFlightTaskID != "tsk_new" || m.followUps.len() != 1 || m.input.Value() != "" {
		t.Fatalf("stale cancel mutated active flow: task=%q queue=%#v input=%q", m.inFlightTaskID, m.followUps.drafts, m.input.Value())
	}
}

func TestFailedTaskEventRestoresQueuedDrafts(t *testing.T) {
	m, _ := newTestModel(nil)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "first", Paste: []string{"p1"}})
	m.followUps.enqueue(promptDraft{Text: "second", Paste: []string{"p2"}})
	m.input.SetValue("current")
	m.pendingPaste = []string{"p3"}

	m.Update(taskCompletedEvent("tsk_running", "failed"))
	assertRestoredQueue(t, m)
}

func TestAutoSubmitFailureRestoresEntireQueueAndCurrentDraft(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("scheduler unavailable")}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "first", Paste: []string{"p1"}})
	m.followUps.enqueue(promptDraft{Text: "second", Paste: []string{"p2"}})
	m.input.SetValue("current")
	m.pendingPaste = []string{"p3"}

	_, cmd := m.Update(taskCompletedEvent("tsk_running", "completed"))
	if cmd == nil {
		t.Fatal("completion did not attempt automatic submission")
	}
	drain(m, cmd)
	assertRestoredQueue(t, m)
	if !strings.Contains(transcriptText(m), "scheduler unavailable") {
		t.Fatalf("automatic submission failure not visible:\n%s", transcriptText(m))
	}
}

func TestQueuedShellUsesGovernedCommandAndThenContinuesFIFO(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"command.exec": map[string]any{"exit_code": 0},
		"task.submit":  map[string]any{"task_id": "tsk_after_shell"},
	}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "!go test ./..."})
	m.followUps.enqueue(promptDraft{Text: "normal follow-up"})

	_, cmd := m.Update(taskCompletedEvent("tsk_running", "completed"))
	if cmd == nil {
		t.Fatal("queued shell was not scheduled")
	}
	drain(m, cmd)
	if len(fc.calls) != 2 || fc.calls[0].method != "command.exec" || fc.calls[1].method != "task.submit" {
		t.Fatalf("queued shell/FIFO calls = %#v", fc.calls)
	}
	if got := fc.calls[1].params["prompt"]; got != "normal follow-up" {
		t.Fatalf("post-shell follow-up prompt = %#v", got)
	}
}

func TestSafeQueuedSlashRunsLocallyBeforeNextFollowUp(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_after_help"},
	}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "/help"})
	m.followUps.enqueue(promptDraft{Text: "normal follow-up"})

	_, cmd := m.Update(taskCompletedEvent("tsk_running", "completed"))
	if cmd == nil {
		t.Fatal("safe slash did not continue to the next queued task")
	}
	drain(m, cmd)
	if len(fc.calls) != 1 || fc.calls[0].method != "task.submit" {
		t.Fatalf("safe slash made unexpected RPC calls: %#v", fc.calls)
	}
	if !strings.Contains(transcriptText(m), "commands and keybindings") {
		t.Fatalf("queued /help did not run locally:\n%s", transcriptText(m))
	}
}

func TestInteractiveQueuedSlashRestoresWithoutOpeningEditor(t *testing.T) {
	m, _ := newTestModel(nil)
	m.getenv = func(string) string { return "true" }
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "/editor"})
	m.followUps.enqueue(promptDraft{Text: "after editor"})

	_, cmd := m.Update(taskCompletedEvent("tsk_running", "completed"))
	if cmd != nil || m.editor != nil {
		t.Fatalf("interactive queued slash ran in background: cmd=%v editor=%#v", cmd, m.editor)
	}
	if m.followUps.len() != 0 || m.input.Value() != "/editor\nafter editor" {
		t.Fatalf("interactive queue restore = composer %q queue %#v", m.input.Value(), m.followUps.drafts)
	}
	if !strings.Contains(transcriptText(m), "review and run") {
		t.Fatalf("interactive restore hint missing:\n%s", transcriptText(m))
	}
}

func taskCompletedEvent(taskID, status string) EventMsg {
	return EventMsg{Raw: map[string]any{
		"type": "task.completed", "task_id": taskID, "status": status,
	}}
}

func assertRestoredQueue(t *testing.T, m *Model) {
	t.Helper()
	if m.followUps.len() != 0 {
		t.Fatalf("restored queue was not drained: %#v", m.followUps.drafts)
	}
	got := m.currentDraft()
	if got.Text != "first\nsecond\ncurrent" {
		t.Fatalf("restored text order = %q", got.Text)
	}
	wantPaste := []string{"p1", "p2", "p3"}
	if len(got.Paste) != len(wantPaste) {
		t.Fatalf("restored paste = %#v", got.Paste)
	}
	for i := range wantPaste {
		if got.Paste[i] != wantPaste[i] {
			t.Fatalf("restored paste = %#v", got.Paste)
		}
	}
}
