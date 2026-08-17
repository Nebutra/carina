package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNamedRecoverFromProvider(t *testing.T) {
	if got := namedRecoverFromProvider("provider_native_tools_rejected"); got != recoverNativeToolRejected {
		t.Fatalf("native map = %q", got)
	}
	if got := namedRecoverFromProvider(recoverPromptTooLong); got != recoverPromptTooLong {
		t.Fatalf("prompt map = %q", got)
	}
	if got := namedRecoverFromProvider("provider_authentication_failed"); got != "" {
		t.Fatalf("auth must stay a provider code, got %q", got)
	}
}

func TestClassifyPromptTooLongFromHTTPBodyAndString(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"context_length_exceeded","message":"prompt is too long"}}`)),
	}
	err := statusError("openai", resp)
	if strings.Contains(err.Error(), "prompt is too long") {
		t.Fatalf("HTTP body leaked into status error: %v", err)
	}
	info := classifyProviderError(err)
	if info.Code != recoverPromptTooLong || info.Category != "invalid_input" || info.Retryable {
		t.Fatalf("HTTP prompt-too-long = %+v", info)
	}
	if got := operatorFacingReasonerError(err); !strings.Contains(strings.ToLower(got), "too long") {
		t.Fatalf("operator copy = %q", got)
	}

	info = classifyProviderError(errors.New("reasoner failed: context_length_exceeded"))
	if info.Code != recoverPromptTooLong {
		t.Fatalf("string prompt-too-long = %+v", info)
	}
}

func TestTranscriptHasToolObservationIgnoresSystemTurns(t *testing.T) {
	tr := &Transcript{Turns: []Turn{{Tool: "system"}, {Tool: "done"}}}
	if transcriptHasToolObservation(tr) {
		t.Fatal("system/done must not count as tool observations")
	}
	tr.Turns = append(tr.Turns, Turn{Tool: "read", Path: "a.txt"})
	if !transcriptHasToolObservation(tr) {
		t.Fatal("read must count as a tool observation")
	}
}

func TestRecoverJournalCapsAndDoctorSnapshot(t *testing.T) {
	var j recoverJournal
	for i := 0; i < maxRecoverJournal+3; i++ {
		j.note(recoverTransition{ReasonCode: recoverEmptyAfterTools, Phase: recoverPhaseRecover, Turn: i})
	}
	got := j.snapshot()
	if len(got) != maxRecoverJournal {
		t.Fatalf("journal len = %d, want %d", len(got), maxRecoverJournal)
	}
	if got[0].Turn != 3 || got[len(got)-1].Turn != maxRecoverJournal+2 {
		t.Fatalf("journal window = %+v", got)
	}

	d := &Daemon{}
	d.noteRecover(recoverNativeToolRejected, recoverPhaseRecover, "run_1", 1)
	report := d.recoverDoctor()
	if report["ok"] != true {
		t.Fatalf("doctor recover ok = %v", report["ok"])
	}
	codes, _ := report["codes"].([]string)
	if len(codes) != 3 {
		t.Fatalf("codes = %v", codes)
	}
	recent, _ := report["recent"].([]recoverTransition)
	if len(recent) != 1 || recent[0].ReasonCode != recoverNativeToolRejected {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestNativeToolRejectRecoverStampsReasonCode(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 5 * time.Millisecond
	defer func() { retryBaseDelay = old }()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "what is this repo")
	d.SetReasoner(&firstErrorReasoner{
		err:  grokCLIError{message: "Grok Build attempted a disabled capability", kind: "json_fallback"},
		then: `{"tool":"done","summary":"Carina is a local-first agent runtime."}`,
	})
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.RunID)
	if tk.Status != "completed" {
		t.Fatalf("recover must complete, got %s reason=%q", tk.Status, tk.Summary)
	}
	outcome := findTaskEvent(t, d, sess.SessionID, task.RunID, "RoutingOutcome")
	if outcome["reason_code"] != recoverNativeToolRejected || outcome["recover"] != true {
		t.Fatalf("RoutingOutcome = %#v", outcome)
	}
	journal := d.recovers.snapshot()
	if len(journal) == 0 || journal[0].ReasonCode != recoverNativeToolRejected || journal[0].Phase != recoverPhaseRecover {
		t.Fatalf("journal = %+v", journal)
	}
}

func TestEmptyAfterToolsTerminalStampsReasonCode(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"read","path":"a.txt"}`,
		`{"tool":`,
		`{"tool":`,
		`{"tool":`,
		`{"tool":`,
	}})
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "read then stall")
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.RunID)
	if tk.Status != "degraded" || !strings.Contains(tk.Summary, "invalid actions") {
		t.Fatalf("task = %+v", tk)
	}
	failed := findTaskEvent(t, d, sess.SessionID, task.RunID, "ExecutionFailed")
	if failed["reason_code"] != recoverEmptyAfterTools {
		t.Fatalf("ExecutionFailed = %#v", failed)
	}
	var recovered bool
	for _, ev := range sessionEvents(t, d, sess.SessionID) {
		if ev.Type != "RoutingOutcome" || ev.TaskID != task.RunID {
			continue
		}
		if ev.Payload["reason_code"] == recoverEmptyAfterTools && ev.Payload["recover"] == true {
			recovered = true
			break
		}
	}
	if !recovered {
		t.Fatal("expected a recovering RoutingOutcome for empty_after_tools")
	}
}

func TestPromptTooLongTerminalStampsReasonCode(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 5 * time.Millisecond
	defer func() { retryBaseDelay = old }()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "too long")
	d.SetReasoner(&failingReasoner{err: providerStatusError{provider: "openai", status: 400, promptTooLong: true}})
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.RunID)
	if tk.Status != "failed" {
		t.Fatalf("status = %s reason=%q", tk.Status, tk.Summary)
	}
	failed := findTaskEvent(t, d, sess.SessionID, task.RunID, "ExecutionFailed")
	if failed["reason_code"] != recoverPromptTooLong {
		t.Fatalf("ExecutionFailed = %#v", failed)
	}
	outcome := findTaskEvent(t, d, sess.SessionID, task.RunID, "RoutingOutcome")
	if outcome["reason_code"] != recoverPromptTooLong || outcome["recover"] == true {
		t.Fatalf("RoutingOutcome = %#v", outcome)
	}
}

func TestDoctorRecoverRecentUsesJournal(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.noteRecover(recoverPromptTooLong, recoverPhaseTerminal, "run_x", 2)
	res, err := d.handleDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	report := res.(map[string]any)
	raw, err := json.Marshal(report["recover"])
	if err != nil {
		t.Fatal(err)
	}
	var recover map[string]any
	if err := json.Unmarshal(raw, &recover); err != nil {
		t.Fatal(err)
	}
	if recover["ok"] != true {
		t.Fatalf("recover = %s", raw)
	}
	recent, _ := recover["recent"].([]any)
	if len(recent) != 1 {
		t.Fatalf("recent = %s", raw)
	}
	row := recent[0].(map[string]any)
	if row["reason_code"] != recoverPromptTooLong || row["phase"] != recoverPhaseTerminal {
		t.Fatalf("row = %#v", row)
	}
}

type sessionEvent struct {
	TaskID  string         `json:"task_id"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

func sessionEvents(t *testing.T, d *Daemon, sessionID string) []sessionEvent {
	t.Helper()
	raw, err := d.kern.ReadEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var events []sessionEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	return events
}

func findTaskEvent(t *testing.T, d *Daemon, sessionID, runID, eventType string) map[string]any {
	t.Helper()
	for _, ev := range sessionEvents(t, d, sessionID) {
		if ev.Type == eventType && ev.TaskID == runID {
			return ev.Payload
		}
	}
	t.Fatalf("%s not found for %s", eventType, runID)
	return nil
}
