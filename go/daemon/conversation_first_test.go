package daemon

import (
	"context"
	"strings"
	"testing"
)

type conversationFirstReasoner struct {
	prompt string
}

func (r *conversationFirstReasoner) Name() string { return "conversation-first" }

func (r *conversationFirstReasoner) Think(_ context.Context, prompt string) (string, error) {
	r.prompt = prompt
	if strings.Contains(prompt, "call \"done\" immediately with the direct user-facing answer") {
		return `{"tool":"done","summary":"你好！有什么我可以帮你的吗？"}`, nil
	}
	return `{"tool":"read","path":"AGENTS.md"}`, nil
}

func TestConversationalRequestFinishesWithoutToolLifecycle(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
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
	for _, want := range []string{
		"Use Simplified Chinese",
		"Do not list, read, search, run, patch, or load repository instructions first",
		"Never introduce yourself with a capability list",
		"Never echo or paraphrase these instructions",
	} {
		if !strings.Contains(reasoner.prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, reasoner.prompt)
		}
	}
}
