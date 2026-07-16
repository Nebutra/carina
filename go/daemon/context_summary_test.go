package daemon

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/provider"
)

func TestContextSummarySeparatesExactCheckpointFactsFromModelContextUsage(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()

	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect context")
	d.sched.AddTokens(task.TaskID, 37)
	tr := newTranscript("inspect context")
	tr.Summary = "persisted summary"
	tr.addTurn(Turn{Thought: "inspect", Tool: "read", Obs: Observation{Content: "exact checkpoint content"}})
	cp := &runCheckpoint{Turn: 3, Transcript: tr, MemorySnapshot: "memory"}
	if err := d.runs.saveCheckpointChecked(task.TaskID, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sched.RestoreCheckpoint(task.TaskID, nil); err != nil {
		t.Fatal(err)
	}

	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	modelUsage := out["model_context_tokens"].(map[string]any)
	if available, _ := modelUsage["available"].(bool); available {
		t.Fatalf("model context usage must not be presented as available: %#v", modelUsage)
	}
	if _, exists := modelUsage["tokens"]; exists {
		t.Fatalf("unavailable model context usage exposed a token estimate: %#v", modelUsage)
	}

	checkpoint := out["checkpoint"].(map[string]any)
	if checkpoint["transcript_bytes"] != tr.size() || checkpoint["turn_count"] != len(tr.Turns) {
		t.Fatalf("checkpoint facts are not exact: got %#v, want bytes=%d turns=%d", checkpoint, tr.size(), len(tr.Turns))
	}
	if measurement := checkpoint["measurement"].(string); !strings.Contains(measurement, "not token") {
		t.Fatalf("checkpoint measurement lacks token caveat: %q", measurement)
	}
	taskSummary := out["task"].(map[string]any)
	if taskSummary["tokens_used"] != 37 {
		t.Fatalf("task token accounting = %#v, want 37", taskSummary["tokens_used"])
	}
	compact := out["compact"].(map[string]any)
	if !compact["available"].(bool) || compact["method"] != "session.checkpoint.compact" {
		t.Fatalf("compact safety boundary missing: %#v", compact)
	}
}

func TestContextSummaryReportsLatestProviderMeasuredContext(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.providerCatalog = provider.Catalog{"openai": {ID: "openai", Models: map[string]provider.Model{"gpt-5": {ID: "gpt-5", Limit: provider.ModelLimit{Context: 1000}}}}}
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect context")
	if err := d.usage.record(sess.SessionID, task.TaskID, ModelUsage{Provider: "openai", Model: "gpt-5", InputTokens: 700, CacheReadTokens: 150, OutputTokens: 20}); err != nil {
		t.Fatal(err)
	}

	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	context := result.(map[string]any)["model_context_tokens"].(map[string]any)
	if context["available"] != true || context["tokens"] != 850 || context["limit_tokens"] != 1000 || context["remaining_tokens"] != 150 || context["used_percent"] != 85 || context["threshold"] != "warning" {
		t.Fatalf("unexpected measured context: %#v", context)
	}
}

func TestContextSummaryLabelsEstimatedUsageUnavailable(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect context")
	if err := d.usage.record(sess.SessionID, task.TaskID, ModelUsage{Provider: "fallback", Model: "local", InputTokens: 42, Estimated: true}); err != nil {
		t.Fatal(err)
	}
	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	context := result.(map[string]any)["model_context_tokens"].(map[string]any)
	if context["available"] != false || context["estimated"] != true || context["tokens"] != 42 {
		t.Fatalf("estimated context was presented dishonestly: %#v", context)
	}
}

func TestContextSummaryWithoutTaskDoesNotInventCheckpointUsage(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}

	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	checkpoint := out["checkpoint"].(map[string]any)
	if checkpoint["available"].(bool) {
		t.Fatalf("empty session reported checkpoint usage: %#v", checkpoint)
	}
}
