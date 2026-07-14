package tui

import (
	"errors"
	"os"
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

func TestAutoSubmitFailureRecallsFrontForIdempotentRetry(t *testing.T) {
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
	if got := draftPrompt(m.currentDraft()); got != "first\np1" || m.followUps.len() != 2 || m.retrySubmission == nil {
		t.Fatalf("unacknowledged queue recall = prompt %q queue %#v retry %#v", got, m.followUps.drafts, m.retrySubmission)
	}
	firstID, _ := fc.calls[0].params["client_submission_id"].(string)
	fc.handler["task.submit"] = map[string]any{"task_id": "tsk_retry", "status": "running"}
	drain(m, m.submit())
	secondID, _ := fc.calls[1].params["client_submission_id"].(string)
	if firstID == "" || secondID != firstID || m.retrySubmission != nil {
		t.Fatalf("retry ids = %q then %q, retry=%#v", firstID, secondID, m.retrySubmission)
	}
	if !strings.Contains(transcriptText(m), "scheduler unavailable") {
		t.Fatalf("automatic submission failure not visible:\n%s", transcriptText(m))
	}
}

func TestRetryResponseWithTerminalStatusDoesNotCreateGhostTask(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("read timeout")}}
	m, _ := newTestModel(fc)
	m.input.SetValue("fast task")
	drain(m, m.submit())
	firstID, _ := fc.calls[0].params["client_submission_id"].(string)
	fc.handler["task.submit"] = map[string]any{
		"task_id": "tsk_already_done", "status": "completed",
	}
	drain(m, m.submit())
	secondID, _ := fc.calls[1].params["client_submission_id"].(string)
	if firstID == "" || firstID != secondID || m.inFlightTaskID != "" || m.retrySubmission != nil {
		t.Fatalf("terminal retry reconciliation: ids=%q/%q task=%q retry=%#v",
			firstID, secondID, m.inFlightTaskID, m.retrySubmission)
	}
}

func TestQueuedSubmissionAckDoesNotOwnMatchingCurrentComposer(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_followup"},
	}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "same"})
	composerType(t, m, "same")
	undoDepth := len(m.composerUndo.undo)

	_, cmd := m.Update(taskCompletedEvent("tsk_running", "completed"))
	drain(m, cmd)
	if got := m.input.Value(); got != "same" {
		t.Fatalf("queued ACK cleared matching current composer: %q", got)
	}
	if len(m.composerUndo.undo) != undoDepth {
		t.Fatalf("queued ACK reset current undo stack: %d -> %d", undoDepth, len(m.composerUndo.undo))
	}
	composerKey(t, m, "ctrl+z")
	if got := m.input.Value(); got != "" {
		t.Fatalf("current composer undo stopped working after queued ACK: %q", got)
	}
}

func TestQueuedSubmissionAckPreservesHistoryNavigationScratch(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_followup"},
	}}
	m, _ := newTestModel(fc)
	m.recordHistory(promptDraft{Text: "old"})
	m.historyPos = len(m.history)
	m.input.SetValue("current")
	if !m.moveHistory(-1) || m.input.Value() != "old" {
		t.Fatal("test setup did not enter history navigation")
	}
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "queued"})

	_, cmd := m.Update(taskCompletedEvent("tsk_running", "completed"))
	drain(m, cmd)
	if !m.moveHistory(1) || m.input.Value() != "queued" {
		t.Fatalf("first forward navigation = %q, want queued history entry", m.input.Value())
	}
	if !m.moveHistory(1) || m.input.Value() != "current" {
		t.Fatalf("history scratch after queued ACK = %q, want current", m.input.Value())
	}
}

func TestTerminalEventBeforeSubmitAckReconcilesAndContinuesFIFO(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_fast"},
	}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "first"})
	m.followUps.enqueue(promptDraft{Text: "second"})

	_, submit := m.Update(taskCompletedEvent("tsk_running", "completed"))
	if submit == nil || m.submitting == nil {
		t.Fatal("first queued submission did not start")
	}
	ack := submit()
	if _, cmd := m.Update(taskCompletedEvent("tsk_fast", "completed")); cmd != nil {
		t.Fatal("early terminal event scheduled work before submit ACK")
	}
	_, next := m.Update(ack)
	if next == nil || m.inFlightTaskID != "" || m.submitting == nil || m.followUps.len() != 1 {
		t.Fatalf("early terminal reconciliation stalled FIFO: next=%v inFlight=%q submitting=%#v queue=%#v",
			next != nil, m.inFlightTaskID, m.submitting, m.followUps.drafts)
	}
}

func TestEarlyFailedTerminalRestoresRemainingQueueAfterAck(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_fast_fail"},
	}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "first"})
	m.followUps.enqueue(promptDraft{Text: "second"})

	_, submit := m.Update(taskCompletedEvent("tsk_running", "completed"))
	ack := submit()
	m.Update(taskCompletedEvent("tsk_fast_fail", "failed"))
	_, next := m.Update(ack)
	if next != nil || m.inFlightTaskID != "" || m.followUps.len() != 0 || draftPrompt(m.currentDraft()) != "second" {
		t.Fatalf("early failure did not restore queue: next=%v task=%q queue=%#v input=%q",
			next != nil, m.inFlightTaskID, m.followUps.drafts, m.input.Value())
	}
}

func TestHistorySearchDefersFailureRestoreUntilSearchCloses(t *testing.T) {
	m, _ := newTestModel(nil)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "queued", Paste: []string{"payload"}})
	m.input.SetValue("original")
	if !m.beginHistorySearch() {
		t.Fatal("history search did not open")
	}

	m.Update(taskCompletedEvent("tsk_running", "failed"))
	if m.followUps.len() != 1 || m.queueRestoreReason == "" {
		t.Fatalf("search did not hold queued restore: queue=%#v reason=%q", m.followUps.drafts, m.queueRestoreReason)
	}
	if cmd, handled := m.handleKey("esc"); !handled || cmd != nil {
		t.Fatalf("closing search restore returned handled=%v cmd=%v", handled, cmd != nil)
	}
	if got := draftPrompt(m.currentDraft()); got != "queued\npayload\noriginal" || m.followUps.len() != 0 {
		t.Fatalf("search close lost deferred drafts: prompt=%q queue=%#v", got, m.followUps.drafts)
	}
}

func TestPasteOnlyRestorePreservesWhitespaceAndDetachedOwnership(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_indent", "status": "running"},
	}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	raw := "    indented\n"
	m.followUps.enqueue(promptDraft{Paste: []string{raw}})
	m.Update(cancelDoneMsg{taskID: "tsk_running"})
	if len(m.pendingPrefix) != 1 || m.pendingPrefix[0] != raw || m.input.Value() != "" {
		t.Fatalf("paste-only restore = prefix %#v input %q", m.pendingPrefix, m.input.Value())
	}
	drain(m, m.submit())
	if got := fc.calls[0].params["prompt"]; got != raw {
		t.Fatalf("restored paste prompt = %#v, want exact %q", got, raw)
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

func TestGovernedCommandParsesQuotedArguments(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"command.exec": map[string]any{"exit_code": 0}}}
	m, _ := newTestModel(fc)
	m.input.SetValue(`!mv "a b" 'c d'`)
	drain(m, m.submit())
	if len(fc.calls) != 1 {
		t.Fatalf("quoted command calls = %#v", fc.calls)
	}
	argv, _ := fc.calls[0].params["argv"].([]any)
	want := []string{"mv", "a b", "c d"}
	if len(argv) != len(want) {
		t.Fatalf("quoted argv = %#v", argv)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("quoted argv = %#v", argv)
		}
	}
}

func TestGovernedCommandParseFailureKeepsDraft(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue(`!echo "unterminated`)
	if cmd := m.submit(); cmd != nil {
		t.Fatal("invalid quoted command scheduled RPC work")
	}
	if m.input.Value() != `!echo "unterminated` || !strings.Contains(transcriptText(m), "command parse failed") {
		t.Fatalf("parse failure was not recoverable: input=%q transcript=%q", m.input.Value(), transcriptText(m))
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
	if cmd != nil || !m.helpOpen || len(fc.calls) != 0 {
		t.Fatalf("queued help did not pause drain: cmd=%v help=%v calls=%#v", cmd != nil, m.helpOpen, fc.calls)
	}
	cmd, _ = m.handleKey("esc")
	if cmd == nil {
		t.Fatal("closing queued help did not resume the next queued task")
	}
	drain(m, cmd)
	if len(fc.calls) != 1 || fc.calls[0].method != "task.submit" {
		t.Fatalf("safe slash made unexpected RPC calls: %#v", fc.calls)
	}
	if m.helpOpen || !strings.Contains(strings.Join(m.helpBodyLines(), "\n"), "commands and keybindings") {
		t.Fatalf("queued /help did not close before drain resumed: open=%v", m.helpOpen)
	}
}

func TestInteractiveQueuedSlashRestoresWithoutOpeningEditor(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_edited", "status": "running"},
	}}
	m, _ := newTestModel(fc)
	m.getenv = func(string) string { return "true" }
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "/editor"})
	m.followUps.enqueue(promptDraft{Text: "after editor"})

	_, cmd := m.Update(taskCompletedEvent("tsk_running", "completed"))
	if cmd != nil || m.editor != nil {
		t.Fatalf("interactive queued slash ran in background: cmd=%v editor=%#v", cmd, m.editor)
	}
	if m.followUps.len() != 1 || m.input.Value() != "/editor" {
		t.Fatalf("interactive queue restore = composer %q queue %#v", m.input.Value(), m.followUps.drafts)
	}
	if !strings.Contains(transcriptText(m), "review and run") {
		t.Fatalf("interactive restore hint missing:\n%s", transcriptText(m))
	}
	cmd = m.submit()
	if cmd == nil || m.editor == nil || m.followUps.len() != 1 || !m.queueRecallPending {
		t.Fatalf("recalled /editor was not executable: cmd=%v editor=%#v queue=%#v", cmd != nil, m.editor, m.followUps.drafts)
	}
	if err := os.WriteFile(m.editor.draft.path, []byte("edited prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, next := m.Update(externalEditorDoneMsg{generation: m.editor.generation}); next != nil ||
		m.input.Value() != "edited prompt" || m.followUps.len() != 1 || !m.queueRecallPending {
		t.Fatalf("editor close released queue early: next=%v input=%q queue=%#v pause=%v",
			next != nil, m.input.Value(), m.followUps.drafts, m.queueRecallPending)
	}
	drain(m, m.submit())
	if len(fc.calls) != 1 || fc.calls[0].params["prompt"] != "edited prompt" || m.followUps.len() != 1 {
		t.Fatalf("edited queue head was not submitted first: calls=%#v queue=%#v", fc.calls, m.followUps.drafts)
	}
}

func TestRecalledEditorFailureRestoresCommandAndKeepsQueuePaused(t *testing.T) {
	m, _ := newTestModel(nil)
	m.getenv = func(string) string { return "true" }
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "/editor"})
	m.followUps.enqueue(promptDraft{Text: "after editor"})
	m.Update(taskCompletedEvent("tsk_running", "completed"))
	cmd := m.submit()
	if cmd == nil || m.editor == nil {
		t.Fatal("recalled editor did not start")
	}
	if _, next := m.Update(externalEditorDoneMsg{generation: m.editor.generation, err: errors.New("cancelled")}); next != nil || m.input.Value() != "/editor" || m.followUps.len() != 1 || !m.queueRecallPending {
		t.Fatalf("editor failure lost queue ownership: next=%v input=%q queue=%#v pause=%v",
			next != nil, m.input.Value(), m.followUps.drafts, m.queueRecallPending)
	}
}

func TestRecalledInteractiveCommandOwnsQueueAcrossAsyncMessages(t *testing.T) {
	m, _ := newTestModel(nil)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "/editor"})
	m.followUps.enqueue(promptDraft{Text: "after editor"})
	m.Update(taskCompletedEvent("tsk_running", "completed"))
	if !m.queueRecallPending || m.followUps.len() != 1 {
		t.Fatalf("interactive recall did not pause queue: pause=%v queue=%#v", m.queueRecallPending, m.followUps.drafts)
	}
	if _, cmd := m.Update(surfaceResultMsg{label: "late", text: "result"}); cmd != nil || m.followUps.len() != 1 {
		t.Fatalf("late surface result bypassed recalled command: cmd=%v queue=%#v", cmd != nil, m.followUps.drafts)
	}
}

func TestUnacknowledgedQueuedSubmissionOwnsQueueAcrossAsyncMessages(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("timeout")}}
	m, _ := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.followUps.enqueue(promptDraft{Text: "first"})
	m.followUps.enqueue(promptDraft{Text: "second"})
	_, cmd := m.Update(taskCompletedEvent("tsk_running", "completed"))
	drain(m, cmd)
	if !m.queueRecallPending || m.followUps.len() != 1 || len(fc.calls) != 1 {
		t.Fatalf("unknown submit did not pause FIFO: pause=%v queue=%#v calls=%#v",
			m.queueRecallPending, m.followUps.drafts, fc.calls)
	}
	if _, cmd := m.Update(clipboardDoneMsg{}); cmd != nil || len(fc.calls) != 1 || m.followUps.len() != 1 {
		t.Fatalf("clipboard completion bypassed unknown submit: cmd=%v calls=%#v queue=%#v",
			cmd != nil, fc.calls, m.followUps.drafts)
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
	want := "first\np1\nsecond\np2\ncurrent\np3"
	if got.Text != "current" || draftPrompt(got) != want {
		t.Fatalf("restored draft = %#v prompt=%q", got, draftPrompt(got))
	}
	if len(got.Prefix) != 2 || got.Prefix[0] != "first\np1" || got.Prefix[1] != "second\np2" ||
		len(got.Paste) != 1 || got.Paste[0] != "p3" {
		t.Fatalf("restored segments = %#v", got)
	}
}
