package daemon

import (
	"strings"
	"testing"
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
	if compact["available"].(bool) || !strings.Contains(compact["reason"].(string), "context.compress") {
		t.Fatalf("compact safety boundary missing: %#v", compact)
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
