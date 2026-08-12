package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseActionBatch(t *testing.T) {
	// A well-formed batch parses with Actions populated.
	a, err := parseAction(`{"intent":"Gather evidence","actions":[{"tool":"read","path":"a.go","intent":"Inspect the source"},{"tool":"search","pattern":"foo","intent":"Find consumers"}]}`)
	if err != nil {
		t.Fatalf("valid batch should parse: %v", err)
	}
	if len(a.Actions) != 2 || a.Actions[0].Tool != "read" || a.Actions[1].Tool != "search" {
		t.Fatalf("batch not parsed: %+v", a.Actions)
	}
	if a.Intent != "Gather evidence" || a.Actions[0].Intent != "Inspect the source" || a.Actions[1].Intent != "Find consumers" {
		t.Fatalf("batch intent was not decoded: %+v", a)
	}

	// A single action still parses with Actions==nil (back-compat).
	s, err := parseAction(`{"tool":"read","path":"x"}`)
	if err != nil || s.Tool != "read" || s.Actions != nil {
		t.Fatalf("single action back-compat broken: %+v err=%v", s, err)
	}

	// A sub-action without a tool errors.
	if _, err := parseAction(`{"actions":[{"path":"a"}]}`); err == nil {
		t.Fatal("batch with a tool-less sub-action must error")
	}
	// A nested batch errors.
	if _, err := parseAction(`{"actions":[{"tool":"read","path":"a","actions":[{"tool":"read"}]}]}`); err == nil {
		t.Fatal("nested batch must error")
	}
}

func TestParseActionIntentFlatAndNested(t *testing.T) {
	flat, err := parseAction(`{"tool":"read","path":"x","intent":"Inspect x"}`)
	if err != nil || flat.Intent != "Inspect x" {
		t.Fatalf("flat intent: action=%+v err=%v", flat, err)
	}
	nested, err := parseAction(`{"action":{"tool":"read","path":"x","intent":"Inspect nested x"}}`)
	if err != nil || nested.Intent != "Inspect nested x" {
		t.Fatalf("nested intent: action=%+v err=%v", nested, err)
	}
}

func TestToolsHelpRequiresPublicIntentForToolActions(t *testing.T) {
	for _, phrase := range []string{
		`Every tool action except "done" MUST include "intent"`,
		"without secrets, hidden reasoning, commands, paths, or policy metadata",
		"Only list/read/search may appear in a parallel batch",
		"Code-intelligence tools and writes",
	} {
		if !strings.Contains(toolsHelp, phrase) {
			t.Fatalf("toolsHelp missing intent contract %q", phrase)
		}
	}
}

func TestParseActionDeduplicatesRepeatedProviderOutput(t *testing.T) {
	raw := `{"tool":"run","command":["echo","ok"]}{"tool":"run","command":["echo","ok"]}`
	a, err := parseAction(raw)
	if err != nil {
		t.Fatalf("identical repeated actions should parse once: %v", err)
	}
	if a.Tool != "run" || strings.Join(a.Command, " ") != "echo ok" {
		t.Fatalf("unexpected deduplicated action: %+v", a)
	}

	if _, err := parseAction(`{"tool":"read","path":"a"}{"tool":"read","path":"b"}`); err == nil {
		t.Fatal("distinct actions must not be silently collapsed")
	}
}

func TestReadOnlyToolClassification(t *testing.T) {
	for _, tool := range []string{"list", "read", "search", "code.search", "code.map"} {
		if !isReadOnlyTool(tool) {
			t.Errorf("%q should be read-only", tool)
		}
	}
	for _, tool := range []string{"patch", "run", "spawn", "mcp"} {
		if isReadOnlyTool(tool) {
			t.Errorf("%q must not be read-only", tool)
		}
	}
	for _, tool := range []string{"list", "read", "search"} {
		if !isParallelBatchTool(tool) {
			t.Errorf("%q should be parallel-batch safe", tool)
		}
	}
	bad := nonParallelBatchTools([]action{{Tool: "read"}, {Tool: "code.map"}, {Tool: "patch"}})
	if len(bad) != 2 || bad[0] != "code.map" || bad[1] != "patch" {
		t.Fatalf("nonParallelBatchTools wrong: %v", bad)
	}
	if len(nonParallelBatchTools([]action{{Tool: "read"}, {Tool: "search"}})) != 0 {
		t.Fatal("a list/read/search batch must have no offenders")
	}
}

func TestSystemPromptRequiresEconomicalCompletion(t *testing.T) {
	for _, instruction := range []string{
		"batch all independent list/read/search actions",
		"use \"done\" immediately",
		"Do not spend another turn rereading unchanged files",
	} {
		if !strings.Contains(systemPrompt, instruction) {
			t.Fatalf("system prompt is missing execution-economy instruction %q", instruction)
		}
	}
}

func TestSystemPromptCarriesNebutraCarinaProductIdentity(t *testing.T) {
	for _, want := range []string{
		"You are Carina",
		"Nebutra",
		"云毓智能",
		"PRODUCT CAPABILITY BRIEF",
		"transactional file patches",
		"hash-chained audit",
		"not: a full IDE",
		"Do not self-identify as Claude, Codex, GPT",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("product identity/capability harness missing %q", want)
		}
	}
	// Subagent tool sheet must stay lean: full product brief lives on main agent only.
	if strings.Contains(toolsHelp, "hash-chained audit") || strings.Contains(toolsHelp, "BYOK") {
		t.Fatal("toolsHelp must not embed the full product capability brief (subagents share toolsHelp)")
	}
	if !strings.Contains(productCapabilityBrief, "hash-chained audit") {
		t.Fatal("productCapabilityBrief missing expected capability content")
	}
	if strings.HasPrefix(strings.TrimSpace(systemPrompt), "You are a coding agent") {
		t.Fatal("system prompt still opens with generic coding-agent identity")
	}
	specs := builtinAgentSpecs()
	if !strings.Contains(specs["build"].SystemPrompt, "You are Carina in build mode") {
		t.Fatalf("build mode should name Carina: %q", specs["build"].SystemPrompt)
	}
}

func TestExecuteBatchParallelReads(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("alpha\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "b.txt"), []byte("bravo\n"), 0o600)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "batch")

	obs := d.executeBatch(sess, task, []action{
		{Tool: "read", Path: "a.txt"},
		{Tool: "read", Path: "b.txt"},
	})
	// Both reads' observations are present, in emit order, labeled.
	if !strings.Contains(obs, "alpha") || !strings.Contains(obs, "bravo") {
		t.Fatalf("batch must contain both reads, got: %s", obs)
	}
	if strings.Index(obs, "[0]") > strings.Index(obs, "[1]") {
		t.Fatalf("batch observations must be in emit order: %s", obs)
	}
}

func TestExecuteBatchRejectsSemanticToolsBeforeLifecycle(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "batch")

	obs := d.executeBatch(sess, task, []action{{Tool: "list"}, {Tool: "code.map"}})
	if !strings.Contains(obs, "parallel batch rejected") || !strings.Contains(obs, "code.map") {
		t.Fatalf("semantic batch must fail closed, got: %s", obs)
	}
	events, err := d.store.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.TaskID == task.RunID {
			t.Fatalf("rejected batch must emit no tool lifecycle, got: %+v", event)
		}
	}
}
