package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
)

func TestCmdImportListIsReadOnlyAndExplainsCopySemantics(t *testing.T) {
	server := rpc.NewServer()
	var got map[string]any
	if err := server.RegisterMethod(rpc.MethodDescriptor{Method: "conversation.import.discover", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &got); err != nil {
			return nil, err
		}
		return map[string]any{
			"copy_semantics": "Carina copies local history and leaves source files unchanged.",
			"conversations": []map[string]any{{
				"source": "codex", "id": "codex-1", "title": "Review auth",
				"workspace_root": "/repo", "target_workspace": "/repo",
				"message_count": 4, "new_messages": 4, "importable": true,
			}},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	client := dialTestServer(t, server)
	defer client.Close()
	out, err := captureStdout(t, func() error { return cmdImport(client, []string{"list", "--source", "codex", "--workspace", "/repo"}) })
	if err != nil {
		t.Fatal(err)
	}
	if sources, ok := got["sources"].([]any); !ok || len(sources) != 1 || sources[0] != "codex" {
		t.Fatalf("discover params = %#v", got)
	}
	if _, ok := got["source_root"]; ok {
		t.Fatalf("empty optional source_root must be omitted: %#v", got)
	}
	for _, want := range []string{"leaves source files unchanged", "codex-1", "Review auth", "4 messages", "not imported"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestCmdImportApplyDiscoversThenAppliesSelectedConversation(t *testing.T) {
	server := rpc.NewServer()
	var applied map[string]any
	if err := server.RegisterMethod(rpc.MethodDescriptor{Method: "conversation.import.discover", Scope: rpc.ScopeRead, Remote: true}, func(json.RawMessage) (any, error) {
		return map[string]any{"conversations": []map[string]any{{
			"source": "claude-code", "id": "claude-1", "path": "/history/claude.jsonl",
			"workspace_root": "/repo", "target_workspace": "/repo", "title": "Fix parser",
			"message_count": 2, "new_messages": 2, "importable": true,
		}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterMethod(rpc.MethodDescriptor{Method: "conversation.import.apply", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &applied); err != nil {
			return nil, err
		}
		return map[string]any{"results": []map[string]any{{
			"source": "claude-code", "conversation_id": "claude-1", "session_id": "sess_1",
			"status": "imported", "imported_messages": 2,
		}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	client := dialTestServer(t, server)
	defer client.Close()
	out, err := captureStdout(t, func() error {
		return cmdImport(client, []string{"apply", "--source", "claude-code", "--id", "claude-1", "--workspace", "/repo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	selections, ok := applied["selections"].([]any)
	if !ok || len(selections) != 1 {
		t.Fatalf("apply params = %#v", applied)
	}
	selection := selections[0].(map[string]any)
	if selection["path"] != "/history/claude.jsonl" || selection["conversation_id"] != "claude-1" || selection["target_workspace"] != "/repo" {
		t.Fatalf("selection = %#v", selection)
	}
	if _, ok := selection["source_root"]; ok {
		t.Fatalf("empty optional source_root must be omitted: %#v", selection)
	}
	for _, want := range []string{"Source files stay unchanged", "Importing 1 conversation", "session=sess_1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("apply output missing %q:\n%s", want, out)
		}
	}
}

func TestParseImportOptionsProtectsScopeAndSelection(t *testing.T) {
	if _, err := parseImportOptions([]string{"--source-root", "/tmp/history"}); err == nil {
		t.Fatal("source root without source must fail")
	}
	if _, err := parseImportOptions([]string{"--all", "--id", "one"}); err == nil {
		t.Fatal("all and id must be exclusive")
	}
	options, err := parseImportOptions([]string{"--source", "codex", "--all-workspaces", "--id", "one", "--id", "two", "--json"})
	if err != nil || options.Source != "codex" || !options.AllWorkspaces || !options.JSON || len(options.IDs) != 2 {
		t.Fatalf("options = %+v, %v", options, err)
	}
}

func TestSelectImportCandidatesSkipsUnavailableAndUpToDateForAll(t *testing.T) {
	candidates := []importCandidate{
		{ID: "new", Importable: true, NewMessages: 2},
		{ID: "current", Importable: true},
		{ID: "missing", ImportError: "target workspace is missing", NewMessages: 1},
	}
	selected, err := selectImportCandidates(candidates, importOptions{All: true})
	if err != nil || len(selected) != 1 || selected[0].ID != "new" {
		t.Fatalf("selected = %+v, %v", selected, err)
	}
	if _, err := selectImportCandidates(candidates, importOptions{IDs: []string{"missing"}}); err == nil || !strings.Contains(err.Error(), "target workspace is missing") {
		t.Fatalf("unavailable selection error = %v", err)
	}
	if _, err := selectImportCandidates(candidates, importOptions{IDs: []string{"current"}}); err == nil || !strings.Contains(err.Error(), "already up to date") {
		t.Fatalf("up-to-date selection error = %v", err)
	}
}
