package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
)

// TestStructuredOutputValidation: a task requiring a JSON output with a key
// rejects a plain-text "done" and completes once the model emits valid JSON.
func TestStructuredOutputValidation(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 5 * time.Millisecond
	defer func() { retryBaseDelay = old }()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "produce json")
	d.sched.SetOutputSchema(task.RunID, json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`))

	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"done","summary":"just some prose, not json"}`, // rejected
		`{"tool":"done","summary":"{\"answer\":42}"}`,           // accepted
	}})
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.RunID)
	if tk.Status != "completed" {
		t.Fatalf("should complete after valid JSON, got %s", tk.Status)
	}
	if !strings.Contains(tk.Summary, "answer") {
		t.Fatalf("final summary should be the valid JSON, got %q", tk.Summary)
	}
}

func TestActiveOutputSchemaIgnoresInertPayloads(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("null"), json.RawMessage("{}"), json.RawMessage("[]")} {
		if got := activeOutputSchema(raw); got != nil {
			t.Fatalf("inert schema %q must be ignored, got %s", raw, got)
		}
	}
	real := json.RawMessage(`{"type":"object","required":["answer"]}`)
	if got := activeOutputSchema(real); string(got) != string(real) {
		t.Fatalf("real schema was dropped: %s", got)
	}
}

func TestConversationalProseAfterToolsIsAcceptedAsDone(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 5 * time.Millisecond
	defer func() { retryBaseDelay = old }()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("Carina is a local agent runtime.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "这个repo是什么")
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"read","path":"README.md","intent":"Identify the project"}`,
		"Carina is a local-first agent runtime for this repository.",
	}})
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.RunID)
	if tk.Status != "completed" {
		t.Fatalf("prose after a successful tool must complete, got %s reason=%q", tk.Status, tk.Summary)
	}
	if !strings.Contains(tk.Summary, "local-first agent runtime") {
		t.Fatalf("salvaged answer missing, got %q", tk.Summary)
	}
}

func TestConversationalSalvageDoesNotBypassOutputSchema(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 5 * time.Millisecond
	defer func() { retryBaseDelay = old }()

	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "produce json")
	d.sched.SetOutputSchema(task.RunID, json.RawMessage(`{"type":"object","required":["answer"]}`))
	d.SetReasoner(&scriptedReasoner{steps: []string{
		"this is prose, not the required object",
		"still prose",
		"and again",
		"last prose",
	}})
	d.runTask(sess, task)

	tk, _ := d.sched.Get(task.RunID)
	if tk.Status == "completed" {
		t.Fatalf("structured-output runs must not salvage prose: %+v", tk)
	}
}

func TestSalvageConversationalDoneGuards(t *testing.T) {
	if _, ok := salvageConversationalDone("hello", nil); ok {
		t.Fatal("nil task must not salvage")
	}
	task := &scheduler.ExecutionRun{}
	got, ok := salvageConversationalDone("Carina is a local-first runtime.", task)
	if !ok || got.Tool != "done" || !strings.Contains(got.Summary, "local-first") || got.ResultKind != "answer" {
		t.Fatalf("plain prose must salvage: %+v ok=%v", got, ok)
	}
	if _, ok := salvageConversationalDone(`{"tool":"read","path":"README.md"}`, task); ok {
		t.Fatal("action-shaped text must requery, not salvage")
	}
	if _, ok := salvageConversationalDone("", task); ok {
		t.Fatal("empty text must not salvage")
	}
	task.OutputSchema = json.RawMessage(`{"type":"object","required":["answer"]}`)
	if _, ok := salvageConversationalDone("still prose", task); ok {
		t.Fatal("active output schema must block salvage")
	}
	task.OutputSchema = nil
	task.SuccessCriteria = []scheduler.SuccessCheck{{Kind: "file_exists", Path: "never.txt"}}
	if _, ok := salvageConversationalDone("still prose", task); ok {
		t.Fatal("success criteria must block salvage")
	}
}
