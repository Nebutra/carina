package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseActionBatch(t *testing.T) {
	// A well-formed batch parses with Actions populated.
	a, err := parseAction(`{"actions":[{"tool":"read","path":"a.go"},{"tool":"search","pattern":"foo"}]}`)
	if err != nil {
		t.Fatalf("valid batch should parse: %v", err)
	}
	if len(a.Actions) != 2 || a.Actions[0].Tool != "read" || a.Actions[1].Tool != "search" {
		t.Fatalf("batch not parsed: %+v", a.Actions)
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
	for _, tool := range []string{"list", "read", "search"} {
		if !isReadOnlyTool(tool) {
			t.Errorf("%q should be read-only", tool)
		}
	}
	for _, tool := range []string{"patch", "run", "spawn", "mcp"} {
		if isReadOnlyTool(tool) {
			t.Errorf("%q must not be read-only", tool)
		}
	}
	bad := nonReadOnlyTools([]action{{Tool: "read"}, {Tool: "patch"}, {Tool: "run"}})
	if len(bad) != 2 || bad[0] != "patch" || bad[1] != "run" {
		t.Fatalf("nonReadOnlyTools wrong: %v", bad)
	}
	if len(nonReadOnlyTools([]action{{Tool: "read"}, {Tool: "search"}})) != 0 {
		t.Fatal("an all-read-only batch must have no offenders")
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
