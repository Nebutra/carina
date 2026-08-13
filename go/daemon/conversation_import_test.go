package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestConversationImportApplyProjectsAndReimportsIncrementally(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "conversation.jsonl")
	writeClaudeImportFixture(t, sourcePath, workspace,
		claudeImportMessage{"user", "u1", "2026-08-13T08:00:00Z", "Inspect the parser."},
		claudeImportMessage{"assistant", "a1", "2026-08-13T08:01:00Z", "The parser is bounded."},
	)

	first := applyClaudeConversationImport(t, d, sourceRoot, sourcePath, workspace)
	if first.Status != "imported" || first.ImportedMessages != 2 || first.SkippedMessages != 0 || first.SessionID == "" {
		t.Fatalf("fresh import receipt = %+v", first)
	}
	assertImportedSessionItems(t, d, first.SessionID, []string{"Inspect the parser.", "The parser is bounded."})
	if report, err := d.kern.AuditVerify(first.SessionID); err != nil {
		t.Fatalf("imported audit verification = %s, %v", report, err)
	} else {
		var verification struct {
			OK       bool    `json:"ok"`
			BrokenAt *string `json:"broken_at"`
		}
		if err := json.Unmarshal(report, &verification); err != nil || !verification.OK || verification.BrokenAt != nil {
			t.Fatalf("imported audit verification = %+v, %v", verification, err)
		}
	}

	unchanged := applyClaudeConversationImport(t, d, sourceRoot, sourcePath, workspace)
	if unchanged.Status != "up_to_date" || unchanged.SessionID != first.SessionID || unchanged.ImportedMessages != 0 || unchanged.SkippedMessages != 2 {
		t.Fatalf("unchanged import receipt = %+v", unchanged)
	}
	assertImportedSessionItems(t, d, first.SessionID, []string{"Inspect the parser.", "The parser is bounded."})

	writeClaudeImportFixture(t, sourcePath, workspace,
		claudeImportMessage{"user", "u1", "2026-08-13T08:00:00Z", "Inspect the parser."},
		claudeImportMessage{"assistant", "a1", "2026-08-13T08:01:00Z", "The parser is bounded."},
		claudeImportMessage{"user", "u2", "2026-08-13T08:02:00Z", "Add a regression test."},
	)
	updated := applyClaudeConversationImport(t, d, sourceRoot, sourcePath, workspace)
	if updated.Status != "updated" || updated.SessionID != first.SessionID || updated.ImportedMessages != 1 || updated.SkippedMessages != 2 {
		t.Fatalf("append-only import receipt = %+v", updated)
	}
	assertImportedSessionItems(t, d, first.SessionID, []string{"Inspect the parser.", "The parser is bounded.", "Add a regression test."})
}

func TestConversationImportFencesConcurrentTaskContext(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "conversation.jsonl")
	writeClaudeImportFixture(t, sourcePath, workspace,
		claudeImportMessage{"user", "u1", "2026-08-13T08:00:00Z", "FIRST IMPORTED MESSAGE"},
		claudeImportMessage{"assistant", "a1", "2026-08-13T08:01:00Z", "SECOND IMPORTED MESSAGE"},
	)

	firstPublished := make(chan string, 1)
	releaseImport := make(chan struct{})
	var blockFirst sync.Once
	d.events.Tap(func(sessionID string, event map[string]any) {
		if event["type"] != "ConversationImported" {
			return
		}
		blockFirst.Do(func() {
			firstPublished <- sessionID
			<-releaseImport
		})
	})
	importDone := make(chan conversationImportOutcome, 1)
	go func() {
		receipt, err := applyClaudeConversationImportResult(d, sourceRoot, sourcePath, workspace)
		importDone <- conversationImportOutcome{receipt: receipt, err: err}
	}()

	var sessionID string
	select {
	case sessionID = <-firstPublished:
	case <-time.After(5 * time.Second):
		t.Fatal("import did not reach the first durable message")
	}
	fence := d.sessionExecutionFence(sessionID)
	if fence.TryRLock() {
		fence.RUnlock()
		close(releaseImport)
		t.Fatal("import released the session execution fence between message appends")
	}

	reasoner := &importPromptReasoner{prompt: make(chan string, 1)}
	d.SetReasoner(reasoner)
	submitStarted := make(chan struct{})
	submitDone := make(chan taskSubmitOutcome, 1)
	go func() {
		close(submitStarted)
		result, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
			"session_id": sessionID,
			"prompt":     "Continue from the imported conversation.",
		}))
		submitDone <- taskSubmitOutcome{result: result, err: err}
	}()
	<-submitStarted

	raw, err := d.kern.ReadEvents(sessionID)
	if err != nil {
		close(releaseImport)
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"type":"ConversationImported"`) != 1 || strings.Contains(string(raw), `"type":"ExecutionQueued"`) {
		close(releaseImport)
		t.Fatalf("mid-batch audit state crossed into submission: %s", raw)
	}
	close(releaseImport)

	select {
	case outcome := <-importDone:
		if outcome.err != nil || outcome.receipt.Status != "imported" || outcome.receipt.ImportedMessages != 2 {
			t.Fatalf("import receipt = %+v, %v", outcome.receipt, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("import did not finish after releasing its barrier")
	}
	select {
	case outcome := <-submitDone:
		if outcome.err != nil || outcome.result == nil {
			t.Fatalf("submission after import = %+v, %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("submission did not resume after import")
	}
	select {
	case prompt := <-reasoner.prompt:
		for _, want := range []string{"UNTRUSTED IMPORTED CONVERSATION", "FIRST IMPORTED MESSAGE", "SECOND IMPORTED MESSAGE"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("agent context omitted %q:\n%s", want, prompt)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reasoner did not receive the post-import context")
	}
}

func TestConversationImportResumesAfterPartialKernelFailure(t *testing.T) {
	stateDir := t.TempDir()
	d := newDaemonAt(t, stateDir)
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "conversation.jsonl")
	writeClaudeImportFixture(t, sourcePath, workspace,
		claudeImportMessage{"user", "u1", "2026-08-13T08:00:00Z", "Persist this once."},
		claudeImportMessage{"assistant", "a1", "2026-08-13T08:01:00Z", "Resume with this message."},
	)

	var stopKernel sync.Once
	d.events.Tap(func(_ string, event map[string]any) {
		if event["type"] == "ConversationImported" {
			stopKernel.Do(func() { _ = d.kern.Close() })
		}
	})
	partial := applyClaudeConversationImport(t, d, sourceRoot, sourcePath, workspace)
	if partial.Status != "partial" || partial.ImportedMessages != 1 || partial.SkippedMessages != 0 || partial.SessionID == "" || partial.Error == "" {
		t.Fatalf("partial import receipt = %+v", partial)
	}
	// The test already closed the kernel to inject the append failure; daemon
	// shutdown may therefore report the child process was waited once already.
	_ = d.Close()

	d = newDaemonAt(t, stateDir)
	defer d.Close()
	resumed := applyClaudeConversationImport(t, d, sourceRoot, sourcePath, workspace)
	if resumed.Status != "updated" || resumed.SessionID != partial.SessionID || resumed.ImportedMessages != 1 || resumed.SkippedMessages != 1 {
		t.Fatalf("resumed import receipt = %+v", resumed)
	}
	assertImportedSessionItems(t, d, partial.SessionID, []string{"Persist this once.", "Resume with this message."})
}

func TestImportedConversationContextTruncatesOnUTF8Boundary(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "conversation.jsonl")
	writeClaudeImportFixture(t, sourcePath, workspace,
		claudeImportMessage{"user", "u1", "2026-08-13T08:00:00Z", strings.Repeat("界", maxImportedMessageContext)},
	)
	receipt := applyClaudeConversationImport(t, d, sourceRoot, sourcePath, workspace)
	sess, ok := d.store.Get(receipt.SessionID)
	if !ok {
		t.Fatalf("imported session %q was not persisted", receipt.SessionID)
	}
	context := d.importedConversationContext(sess)
	if !utf8.ValidString(context) {
		t.Fatal("imported context split a UTF-8 code point")
	}
	if len(context) > maxImportedContextBytes {
		t.Fatalf("imported context is %d bytes, want at most %d", len(context), maxImportedContextBytes)
	}
	wantRunes := (maxImportedMessageContext - len(importedMessageTruncated)) / len("界")
	if count := strings.Count(context, "界"); count != wantRunes {
		t.Fatalf("retained %d complete multibyte runes, want %d", count, wantRunes)
	}
	if !strings.Contains(context, "[message truncated by Carina]") || !strings.HasSuffix(context, "END UNTRUSTED IMPORTED CONVERSATION") {
		t.Fatalf("truncated imported context lost its framing:\n%s", context)
	}
}

func TestImportedConversationContextKeepsNewestSuffixInSourceOrder(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "conversation.jsonl")
	messages := make([]claudeImportMessage, 0, 10)
	for index := 0; index < 10; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, claudeImportMessage{
			role: role, id: fmt.Sprintf("m%d", index),
			timestamp: fmt.Sprintf("2026-08-13T08:%02d:00Z", index),
			content:   fmt.Sprintf("IMPORT-MARKER-%02d %s", index, strings.Repeat("x", 2200)),
		})
	}
	writeClaudeImportFixture(t, sourcePath, workspace, messages...)
	receipt := applyClaudeConversationImport(t, d, sourceRoot, sourcePath, workspace)
	sess, ok := d.store.Get(receipt.SessionID)
	if !ok {
		t.Fatalf("imported session %q was not persisted", receipt.SessionID)
	}
	context := d.importedConversationContext(sess)
	if len(context) > maxImportedContextBytes {
		t.Fatalf("imported context is %d bytes, want at most %d", len(context), maxImportedContextBytes)
	}
	if !strings.Contains(context, "[earlier imported messages omitted to fit context]") {
		t.Fatalf("bounded context did not disclose omitted history:\n%s", context)
	}
	if strings.Contains(context, "IMPORT-MARKER-00") || !strings.Contains(context, "IMPORT-MARKER-09") {
		t.Fatalf("bounded context did not retain the newest suffix")
	}
	firstRetained := -1
	previousPosition := -1
	for index := 0; index < 10; index++ {
		marker := fmt.Sprintf("IMPORT-MARKER-%02d", index)
		position := strings.Index(context, marker)
		if position < 0 {
			if firstRetained >= 0 {
				t.Fatalf("retained messages are not a contiguous newest suffix; %s is missing", marker)
			}
			continue
		}
		if firstRetained < 0 {
			firstRetained = index
		}
		if position <= previousPosition {
			t.Fatalf("retained source order changed at %s", marker)
		}
		previousPosition = position
	}
	if firstRetained <= 0 || firstRetained >= 9 {
		t.Fatalf("unexpected retained suffix start %d", firstRetained)
	}
}

type claudeImportMessage struct {
	role      string
	id        string
	timestamp string
	content   string
}

func writeClaudeImportFixture(t *testing.T, path, workspace string, messages ...claudeImportMessage) {
	t.Helper()
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		record := map[string]any{
			"type":      message.role,
			"sessionId": "claude-import-1",
			"uuid":      message.id,
			"cwd":       workspace,
			"timestamp": message.timestamp,
			"message": map[string]any{
				"role":    message.role,
				"content": message.content,
			},
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(raw))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func applyClaudeConversationImport(t *testing.T, d *Daemon, sourceRoot, sourcePath, workspace string) conversationImportReceipt {
	t.Helper()
	receipt, err := applyClaudeConversationImportResult(d, sourceRoot, sourcePath, workspace)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func applyClaudeConversationImportResult(d *Daemon, sourceRoot, sourcePath, workspace string) (conversationImportReceipt, error) {
	params, err := json.Marshal(map[string]any{
		"selections": []map[string]any{{
			"source":           "claude-code",
			"source_root":      sourceRoot,
			"path":             sourcePath,
			"conversation_id":  "claude-import-1",
			"target_workspace": workspace,
		}},
	})
	if err != nil {
		return conversationImportReceipt{}, err
	}
	result, err := d.handleConversationImportApply(params)
	if err != nil {
		return conversationImportReceipt{}, err
	}
	receipts, ok := result.(map[string]any)["results"].([]conversationImportReceipt)
	if !ok || len(receipts) != 1 {
		return conversationImportReceipt{}, fmt.Errorf("unexpected import result: %#v", result)
	}
	return receipts[0], nil
}

func assertImportedSessionItems(t *testing.T, d *Daemon, sessionID string, contents []string) {
	t.Helper()
	result, err := d.handleSessionItems(mustJSON(t, map[string]any{"session_id": sessionID}))
	if err != nil {
		t.Fatal(err)
	}
	items, ok := result.([]SessionItemEvent)
	if !ok {
		t.Fatalf("session.items = %#v", result)
	}
	projected := make([]SessionItemEvent, 0, len(items))
	for _, item := range items {
		if item.Item != nil {
			projected = append(projected, item)
		}
	}
	if len(projected) != len(contents) {
		t.Fatalf("projected session items = %#v", items)
	}
	for index, want := range contents {
		item := projected[index].Item
		if item == nil || item.Status != "completed" || item.Details["content"] != want || item.Details["source"] != "claude-code" || item.Details["source_conversation_id"] != "claude-import-1" || item.Details["imported"] != true || item.Details["fingerprint"] == "" || item.Details["batch_id"] == "" || item.Details["source_path"] == "" || projected[index].SourceEventID == "" {
			t.Fatalf("imported item %d lacks provenance: %+v", index, projected[index])
		}
		wantType := "user"
		if index%2 == 1 {
			wantType = "agent_message"
		}
		if item.Type != wantType {
			t.Fatalf("imported item %d type = %q, want %q", index, item.Type, wantType)
		}
	}
}

type taskSubmitOutcome struct {
	result any
	err    error
}

type conversationImportOutcome struct {
	receipt conversationImportReceipt
	err     error
}

type importPromptReasoner struct {
	prompt chan string
	once   sync.Once
}

func (r *importPromptReasoner) Name() string { return "import-context-test" }

func (r *importPromptReasoner) Think(_ context.Context, prompt string) (string, error) {
	r.once.Do(func() { r.prompt <- prompt })
	return `{"tool":"done","summary":"import context received"}`, nil
}
