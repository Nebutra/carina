package conversationimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeVisibleConversationFiltersInternalRecords(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	path := filepath.Join(root, "claude.jsonl")
	writeLines(t, path,
		`{"type":"user","sessionId":"claude-1","uuid":"u1","cwd":`+quote(workspace)+`,"timestamp":"2026-08-01T10:00:00Z","message":{"role":"user","content":"<system-reminder>secret internal</system-reminder>Fix the parser"}}`,
		`{"type":"assistant","sessionId":"claude-1","uuid":"a1","cwd":`+quote(workspace)+`,"timestamp":"2026-08-01T10:01:00Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"I fixed it."},{"type":"tool_use","name":"Write"}]}}`,
		`{"type":"progress","sessionId":"claude-1","cwd":`+quote(workspace)+`,"data":{"token":"credential"}}`,
		`not-json`,
	)

	conversation, err := Load(SourceClaude, root, path)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.ID != "claude-1" || conversation.WorkspaceRoot != workspace {
		t.Fatalf("metadata = %+v", conversation)
	}
	if conversation.MessageCount != 2 || conversation.Title != "Fix the parser" {
		t.Fatalf("visible projection = %+v", conversation)
	}
	joined := conversation.Messages[0].Content + conversation.Messages[1].Content
	for _, forbidden := range []string{"secret internal", "hidden", "credential", "tool_use"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("import leaked %q: %s", forbidden, joined)
		}
	}
	if len(conversation.Warnings) != 1 {
		t.Fatalf("warnings = %#v", conversation.Warnings)
	}
}

func TestCodexPrefersVisibleEventsOverDuplicateResponseItems(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	path := filepath.Join(root, "codex.jsonl")
	writeLines(t, path,
		`{"type":"session_meta","timestamp":"2026-08-01T10:00:00Z","payload":{"id":"codex-1","cwd":`+quote(workspace)+`}}`,
		`{"type":"response_item","timestamp":"2026-08-01T10:00:01Z","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"internal policy"}]}}`,
		`{"type":"response_item","timestamp":"2026-08-01T10:00:02Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Review auth"}]}}`,
		`{"type":"event_msg","timestamp":"2026-08-01T10:00:03Z","payload":{"type":"user_message","message":"Review auth"}}`,
		`{"type":"response_item","timestamp":"2026-08-01T10:00:04Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Found one issue."}]}}`,
		`{"type":"event_msg","timestamp":"2026-08-01T10:00:05Z","payload":{"type":"agent_message","message":"Found one issue."}}`,
		`{"type":"response_item","timestamp":"2026-08-01T10:00:06Z","payload":{"type":"reasoning","summary":["hidden reasoning"]}}`,
	)

	conversation, err := Load(SourceCodex, root, path)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.MessageCount != 2 {
		t.Fatalf("messages = %#v", conversation.Messages)
	}
	if conversation.Messages[0].Content != "Review auth" || conversation.Messages[1].Content != "Found one issue." {
		t.Fatalf("visible projection = %#v", conversation.Messages)
	}
}

func TestClaudeSkipsInvalidUTF8AndOversizedVisibleMessages(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	path := filepath.Join(root, "bounded.jsonl")
	oversized := `{"type":"assistant","sessionId":"bounded","uuid":"large","cwd":` + quote(workspace) + `,"message":{"role":"assistant","content":"` + strings.Repeat("x", maxMessageBytes+1) + `"}}`
	valid := `{"type":"user","sessionId":"bounded","uuid":"valid","cwd":` + quote(workspace) + `,"message":{"role":"user","content":"Keep this visible message"}}`
	raw := append([]byte{0xff, '\n'}, []byte(oversized+"\n"+valid+"\n")...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	conversation, err := Load(SourceClaude, root, path)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.ID != "bounded" || conversation.MessageCount != 1 || conversation.Messages[0].Content != "Keep this visible message" {
		t.Fatalf("bounded visible projection = %+v", conversation)
	}
	warnings := strings.Join(conversation.Warnings, "\n")
	for _, want := range []string{"not valid UTF-8", "message is too large"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("warnings missing %q: %s", want, warnings)
		}
	}
}

func TestDiscoverDefaultsToCurrentWorkspaceAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	other := t.TempDir()
	inside := filepath.Join(root, "inside.jsonl")
	writeLines(t, inside, `{"type":"user","sessionId":"one","cwd":`+quote(workspace)+`,"message":{"role":"user","content":"one"}}`)
	writeLines(t, filepath.Join(root, "other.jsonl"), `{"type":"user","sessionId":"two","cwd":`+quote(other)+`,"message":{"role":"user","content":"two"}}`)

	result, err := Discover(Options{Sources: []Source{SourceClaude}, SourceRoot: root, WorkspaceRoot: workspace})
	if err != nil || len(result.Conversations) != 1 || result.Conversations[0].ID != "one" {
		t.Fatalf("discover = %+v, %v", result, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeLines(t, outside, `{"type":"user","sessionId":"outside","cwd":`+quote(workspace)+`,"message":{"role":"user","content":"outside"}}`)
	if _, err := Load(SourceClaude, root, outside); err == nil {
		t.Fatal("expected outside path rejection")
	}
}

func TestCollectSourcePathsEnforcesAggregateByteBudget(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "a.jsonl")
	second := filepath.Join(root, "b.jsonl")
	writeLines(t, first, `{"type":"first"}`)
	writeLines(t, second, `{"type":"second"}`)
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}

	paths, consumed, exhausted, err := collectSourcePaths(root, firstInfo.Size()+secondInfo.Size()-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != first {
		t.Fatalf("bounded paths = %#v, want only %s", paths, first)
	}
	if consumed != firstInfo.Size() || !exhausted {
		t.Fatalf("budget accounting = consumed %d exhausted %v", consumed, exhausted)
	}
}

func TestDiscoverReportsAggregateByteBudgetExhaustion(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	oversized := filepath.Join(root, "oversized.jsonl")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxDiscoveryBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := Discover(Options{
		Sources:       []Source{SourceClaude},
		SourceRoot:    root,
		WorkspaceRoot: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conversations) != 0 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "discovery byte limit reached") {
		t.Fatalf("bounded discovery result = %+v", result)
	}
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
