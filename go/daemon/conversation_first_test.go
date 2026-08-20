package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

type conversationFirstReasoner struct {
	prompt string
}

func (r *conversationFirstReasoner) Name() string { return "conversation-first" }

func (r *conversationFirstReasoner) Think(_ context.Context, prompt string) (string, error) {
	r.prompt = prompt
	if strings.Contains(prompt, "Intent:") && strings.Contains(prompt, "converse:") {
		return `{"tool":"done","summary":"你好！有什么我可以帮你的吗？"}`, nil
	}
	return `{"tool":"read","path":"AGENTS.md"}`, nil
}

func TestConversationalRequestFinishesWithoutToolLifecycle(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("GREETING_MUST_NOT_LOAD_THIS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reasoner := &conversationFirstReasoner{}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	d.sched.SetLocale(task.RunID, "zh")
	task, _ = d.sched.Get(task.RunID)

	var toolEvents []string
	d.events.Tap(func(_ string, event map[string]any) {
		kind, _ := event["type"].(string)
		if strings.HasPrefix(kind, "ToolCall") {
			toolEvents = append(toolEvents, kind)
		}
	})

	d.runTask(sess, task)

	completed, _ := d.sched.Get(task.RunID)
	if completed.Status != "completed" {
		t.Fatalf("task status = %q, want completed", completed.Status)
	}
	if completed.Summary != "你好！有什么我可以帮你的吗？" {
		t.Fatalf("task summary = %q", completed.Summary)
	}
	if len(toolEvents) != 0 {
		t.Fatalf("conversational request emitted tool lifecycle events: %v", toolEvents)
	}
	cp := d.runs.loadCheckpoint(task.RunID)
	if cp == nil || cp.Turn != 1 || cp.Transcript == nil || len(cp.Transcript.Turns) != 1 {
		t.Fatalf("completed conversation checkpoint = %+v", cp)
	}
	final := cp.Transcript.Turns[0]
	if final.Tool != "done" || final.Obs.Content != completed.Summary {
		t.Fatalf("completed conversation checkpoint lost final response: %+v", final)
	}
	forked, err := d.handleSessionFork(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "last_task_id": task.RunID,
	}))
	if err != nil {
		t.Fatalf("fork through completed conversation: %v", err)
	}
	child := forked.(*sessionstore.Session)
	if child.ForkedFromTaskID != task.RunID || child.ForkedThroughTurn != 1 {
		t.Fatalf("fork lineage = %+v", child)
	}
	if !strings.HasPrefix(strings.TrimSpace(reasoner.prompt), "converse:") {
		t.Fatalf("greeting prompt must start with converse mode:\n%s", truncate(reasoner.prompt, 240))
	}
	for _, banned := range []string{
		"PROJECT INSTRUCTIONS",
		"GREETING_MUST_NOT_LOAD_THIS",
		"Inspect the workspace",
		"PRODUCT CAPABILITY BRIEF",
		"hash-chained audit",
		"call \"done\" immediately",
	} {
		if strings.Contains(reasoner.prompt, banned) {
			t.Fatalf("greeting prompt leaked %q:\n%s", banned, reasoner.prompt)
		}
	}
	for _, want := range []string{
		"Use Simplified Chinese",
		"Use tools only when this message needs workspace evidence",
		"Do not recast the operator's question into a product tour",
		"You are Carina",
		"Nebutra",
		"云毓智能",
		"Intent:",
		"situated answer",
		"done ends the turn after you have answered",
		"done.summary is the only user-visible answer",
		"Never put a JSON object",
		"TASK: hi",
	} {
		if !strings.Contains(reasoner.prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, reasoner.prompt)
		}
	}
}

func TestGreetingComposeIsConverseFirstWithoutProjectInstructions(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("GREETING_MUST_NOT_LOAD_THIS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	layers := d.composeAgentPromptLayers(sess, task, "")
	if !strings.HasPrefix(layers.Constitution, "converse:") {
		t.Fatalf("constitution must start with converse mode, got %q", truncate(layers.Constitution, 160))
	}
	if strings.Contains(layers.Workspace, "PROJECT INSTRUCTIONS") || strings.Contains(layers.Workspace, "GREETING_MUST_NOT_LOAD_THIS") {
		t.Fatalf("greeting loaded project instructions:\n%s", layers.Workspace)
	}
	if strings.Contains(layers.Constitution, "PRODUCT CAPABILITY BRIEF") {
		t.Fatalf("greeting constitution must not carry the capability brief:\n%s", truncate(layers.Constitution, 240))
	}
	seg := buildPromptSegmentsFromLayers(layers, task.UserPrompt, "turn1", "GO")
	if strings.Contains(seg.StablePrefix, "TASK:") || strings.Contains(seg.StablePrefix, "turn1") {
		t.Fatalf("TASK/transcript leaked into cache prefix: %q", truncate(seg.StablePrefix, 200))
	}
	if !strings.Contains(seg.VolatileSuffix, "TASK: hi") || !strings.Contains(seg.VolatileSuffix, "turn1") {
		t.Fatalf("volatile suffix missing TASK/transcript: %q", seg.VolatileSuffix)
	}
}

func TestTaskSubmitDefaultsToConverse(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	result, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "prompt": "hi",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := result.(*scheduler.ExecutionRun)
	if task.Agent != defaultInteractiveAgent {
		t.Fatalf("default agent = %q, want %q", task.Agent, defaultInteractiveAgent)
	}
}

func TestUsefulnessComposeOmitsCapabilityBrief(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "你有啥用")
	layers := d.composeAgentPromptLayers(sess, task, "")
	if strings.Contains(layers.Constitution, "PRODUCT CAPABILITY BRIEF") || strings.Contains(layers.Constitution, "hash-chained audit") {
		t.Fatalf("usefulness prompt must not inject the capability brief:\n%s", truncate(layers.Constitution, 400))
	}
	if !strings.Contains(layers.Constitution, "Intent:") || !strings.Contains(layers.Constitution, "situated answer") {
		t.Fatalf("usefulness prompt must carry the intent contract:\n%s", truncate(layers.Constitution, 400))
	}
	if strings.Contains(layers.Workspace, "PROJECT INSTRUCTIONS") {
		t.Fatalf("usefulness prompt loaded project instructions:\n%s", layers.Workspace)
	}
}

func TestRepoWorkComposeLoadsProjectInstructions(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("REPO_RULE_MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	converse := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "fix the parser in agent.go")
	converseLayers := d.composeAgentPromptLayers(sess, converse, "")
	if strings.Contains(converseLayers.Workspace, "PROJECT INSTRUCTIONS") || strings.Contains(converseLayers.Workspace, "REPO_RULE_MARKER") {
		t.Fatalf("converse must not classify utterances to dump project instructions:\n%s", converseLayers.Workspace)
	}
	build := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	build.Agent = "build"
	buildLayers := d.composeAgentPromptLayers(sess, build, "")
	if !strings.Contains(buildLayers.Workspace, "PROJECT INSTRUCTIONS") || !strings.Contains(buildLayers.Workspace, "REPO_RULE_MARKER") {
		t.Fatalf("build mode must load project instructions:\n%s", buildLayers.Workspace)
	}
}
