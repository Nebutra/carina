package tui

import (
	"errors"
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestSubmissionJournalRoundTripAndPermissions(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	journal := newSubmissionJournal(stateDir, workspace)
	retry := submissionRetry{
		clientID: "tui_persisted",
		prompt:   "    exact\nbody",
		draft:    promptDraft{Prefix: []string{"    exact"}, Paste: []string{"body"}},
	}
	if err := journal.save("sess_journal", retry); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(journal.path("sess_journal"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("journal permissions = %o", info.Mode().Perm())
	}
	got, ok, err := journal.load("sess_journal")
	if err != nil || !ok || got.clientID != retry.clientID || got.prompt != retry.prompt || !draftsEqual(got.draft, retry.draft) {
		t.Fatalf("journal load = %#v ok=%v err=%v", got, ok, err)
	}
	if err := journal.clear("sess_journal", "different_id"); err == nil {
		t.Fatal("acknowledgement for a different submission cleared the journal")
	}
	if _, ok, err := journal.load("sess_journal"); err != nil || !ok {
		t.Fatalf("CAS mismatch removed journal: ok=%v err=%v", ok, err)
	}
	if err := journal.clear("sess_journal", retry.clientID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := journal.load("sess_journal"); err != nil || ok {
		t.Fatalf("cleared journal returned ok=%v err=%v", ok, err)
	}
}

func TestSubmissionJournalReconcilesAcrossModelRestart(t *testing.T) {
	stateDir := t.TempDir()
	firstCaller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []any{}},
		"task.submit":    errors.New("read timeout"),
	}}
	m1, err := NewChecked(Options{Theme: theme.New(theme.Mono), StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	m1.Update(SessionReadyMsg{SessionID: "sess_restart", Call: firstCaller})
	m1.input.SetValue("recover after restart")
	drain(m1, m1.submit())
	firstID, _ := firstCaller.last().params["client_submission_id"].(string)
	if firstID == "" || m1.retrySubmission == nil {
		t.Fatal("first model did not persist an unacknowledged submission")
	}
	m1.Close()

	secondCaller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []any{}},
		"task.submit": map[string]any{
			"task_id": "task_existing", "status": "running",
		},
	}}
	m2, err := NewChecked(Options{Theme: theme.New(theme.Mono), StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	_, cmd := m2.Update(SessionReadyMsg{SessionID: "sess_restart", Call: secondCaller})
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("restart reconciliation command = %T", msg)
	}
	for _, child := range batch {
		if child != nil {
			drain(m2, child)
		}
	}
	if len(secondCaller.calls) != 2 {
		t.Fatalf("restart RPC calls = %#v", secondCaller.calls)
	}
	retryCall := secondCaller.calls[1]
	if retryCall.method != "task.submit" || retryCall.params["client_submission_id"] != firstID ||
		retryCall.params["prompt"] != "recover after restart" {
		t.Fatalf("restart retry = %#v, want id %q", retryCall, firstID)
	}
	if m2.retrySubmission != nil || m2.input.Value() != "" {
		t.Fatalf("restart reconciliation did not commit: retry=%#v input=%q", m2.retrySubmission, m2.input.Value())
	}
	if _, ok, err := m2.submissions.load("sess_restart"); err != nil || ok {
		t.Fatalf("acknowledged journal remained: ok=%v err=%v", ok, err)
	}
}

func TestSubmissionLeasePreventsConcurrentSessionWriters(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []any{}},
		"task.submit":    map[string]any{"task_id": "task_1", "status": "running"},
	}}
	m1, err := NewChecked(Options{Theme: theme.New(theme.Mono), StateDir: stateDir, WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer m1.Close()
	m2, err := NewChecked(Options{Theme: theme.New(theme.Mono), StateDir: stateDir, WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	m1.Update(SessionReadyMsg{SessionID: "sess_shared", Call: caller})
	m2.Update(SessionReadyMsg{SessionID: "sess_shared", Call: caller})
	if m1.submissionLeaseErr != nil || m2.submissionLeaseErr == nil {
		t.Fatalf("lease ownership = first %v second %v", m1.submissionLeaseErr, m2.submissionLeaseErr)
	}
	m2.input.SetValue("must not send")
	if cmd := m2.submit(); cmd != nil {
		t.Fatal("read-only TUI scheduled a task submission")
	}
	m1.Close()
	m2.Update(SessionReadyMsg{SessionID: "sess_shared", Call: caller})
	if m2.submissionLeaseErr != nil {
		t.Fatalf("lease was not released with the first model: %v", m2.submissionLeaseErr)
	}
}

func TestJournalReconcilePreservesDraftTypedBeforeConnection(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	journal := newSubmissionJournal(stateDir, workspace)
	retry := submissionRetry{clientID: "tui_old", prompt: "old pending", draft: promptDraft{Text: "old pending"}}
	if err := journal.save("sess_slow", retry); err != nil {
		t.Fatal(err)
	}
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []any{}},
		"task.submit":    map[string]any{"task_id": "task_old", "status": "running"},
	}}
	m, err := NewChecked(Options{Theme: theme.New(theme.Mono), StateDir: stateDir, WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.input.SetValue("new draft typed while connecting")
	m.recordComposerEdit(composerSnapshot{}, composerEditTyping)
	undoDepth := len(m.composerUndo.undo)
	_, cmd := m.Update(SessionReadyMsg{SessionID: "sess_slow", Call: caller})
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("reconcile command = %T", msg)
	}
	for _, child := range batch {
		if child != nil {
			drain(m, child)
		}
	}
	if got := m.input.Value(); got != "new draft typed while connecting" {
		t.Fatalf("connection recovery overwrote current draft: %q", got)
	}
	if len(m.composerUndo.undo) != undoDepth {
		t.Fatalf("background recovery reset current undo stack: %d -> %d", undoDepth, len(m.composerUndo.undo))
	}
}

func TestLatestPendingSubmissionSessionIsWorkspaceScoped(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	other := t.TempDir()
	journal := newSubmissionJournal(stateDir, workspace)
	if err := journal.save("sess_pending", submissionRetry{
		clientID: "tui_pending", prompt: "pending", draft: promptDraft{Text: "pending"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := LatestPendingSubmissionSession(stateDir, workspace); err != nil || got != "sess_pending" {
		t.Fatalf("pending session = %q err=%v", got, err)
	}
	if got, err := LatestPendingSubmissionSession(stateDir, other); err != nil || got != "" {
		t.Fatalf("other workspace inherited pending session %q err=%v", got, err)
	}
}

func TestEquivalentWirePromptReusesUnknownSubmissionID(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("timeout")}}
	m, _ := newTestModel(fc)
	m.input.SetValue("work")
	drain(m, m.submit())
	firstID := fc.calls[0].params["client_submission_id"]
	m.input.SetValue("  work  ")
	fc.handler["task.submit"] = map[string]any{"task_id": "task_same", "status": "running"}
	drain(m, m.submit())
	if len(fc.calls) != 2 || fc.calls[1].params["client_submission_id"] != firstID {
		t.Fatalf("wire-equivalent retry calls = %#v", fc.calls)
	}
}

func TestChangedUnknownSubmissionRequiresExplicitNewIntent(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("timeout")}}
	m, _ := newTestModel(fc)
	m.input.SetValue("first")
	drain(m, m.submit())
	firstID := fc.calls[0].params["client_submission_id"]
	m.input.SetValue("different")
	if cmd := m.submit(); cmd != nil || len(fc.calls) != 1 {
		t.Fatal("changed unknown submission was sent without explicit intent")
	}
	fc.handler["task.submit"] = map[string]any{"task_id": "task_new", "status": "running"}
	cmd, handled := m.handleKey("alt+s")
	if !handled || cmd == nil {
		t.Fatal("explicit new-submission key did not start work")
	}
	drain(m, cmd)
	if fc.calls[1].params["client_submission_id"] == firstID {
		t.Fatalf("explicit new submission reused old id: %#v", fc.calls)
	}
}

func TestTerminalReconcileRejectsLateActiveTaskMessage(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("timeout")}}
	m, _ := newTestModel(fc)
	m.input.SetValue("fast")
	drain(m, m.submit())
	fc.handler["task.submit"] = map[string]any{"task_id": "task_done", "status": "completed"}
	reconcile := m.submit()
	m.Update(TaskActiveMsg{TaskID: "task_done"})
	if m.inFlightTaskID != "task_done" {
		t.Fatal("test setup did not install the active task before reconciliation")
	}
	drain(m, reconcile)
	if m.inFlightTaskID != "" {
		t.Fatalf("terminal reconcile did not clear earlier active task %q", m.inFlightTaskID)
	}
	m.Update(TaskActiveMsg{TaskID: "task_done"})
	if m.inFlightTaskID != "" {
		t.Fatalf("late active message resurrected terminal task %q", m.inFlightTaskID)
	}
}
