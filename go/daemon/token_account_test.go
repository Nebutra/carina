package daemon

import (
	"strings"
	"testing"
)

func TestAccountedTokensPrefersProviderUsage(t *testing.T) {
	usage := ModelUsage{InputTokens: 41, OutputTokens: 3}
	if got := accountedTokens(usage, "abcd"); got != 41 {
		t.Fatalf("provider usage = %d, want 41", got)
	}
	estimated := ModelUsage{InputTokens: 41, Estimated: true}
	if got := accountedTokens(estimated, "abcd"); got != estimateTokens("abcd") {
		t.Fatalf("estimated usage must fall back to chars/4, got %d", got)
	}
	if got := accountedTokens(ModelUsage{}, "abcd"); got != estimateTokens("abcd") {
		t.Fatalf("absent usage must fall back to chars/4, got %d", got)
	}
}

func TestCompactPressureUsesObservedInputTokens(t *testing.T) {
	tr := newTranscript("task")
	tr.policy = CompactionPolicy{MaxChars: 1_000_000, KeepRecent: 2, ToolOutputMax: 100_000, SummarizeAfter: 1, MaxTokens: 50}
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: "small"}})
	if tr.shouldCompact() {
		t.Fatal("chars/4 of a tiny view must stay under MaxTokens=50")
	}
	tr.noteObservedInputTokens(80)
	if !tr.shouldCompact() {
		t.Fatal("provider-reported input tokens above MaxTokens must trigger compact")
	}
	if tr.viewTokens() != 80 {
		t.Fatalf("viewTokens = %d, want 80", tr.viewTokens())
	}
	_ = tr.compact(nil)
	if tr.observedInputTokens != 0 || tr.viewTokens() == 80 {
		t.Fatal("compact must drop stale observed input tokens after the view changes")
	}
}

func TestContextLedgerPrefersProviderUsageForModelView(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect context")
	tr := newTranscript("inspect context")
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: "exact checkpoint content"}})
	if err := d.runs.saveCheckpointChecked(task.RunID, &runCheckpoint{Turn: 1, Transcript: tr}); err != nil {
		t.Fatal(err)
	}
	if err := d.usage.record(sess.SessionID, task.RunID, ModelUsage{Provider: "openai", Model: "gpt-5", InputTokens: 77, OutputTokens: 2}); err != nil {
		t.Fatal(err)
	}
	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	ledger := result.(map[string]any)["ledger"].(map[string]any)
	if ledger["estimate_method"] != "provider_usage" || ledger["estimated"] != false || ledger["model_visible_tokens_estimated"] != 77 {
		t.Fatalf("ledger must use provider input tokens, got %#v", ledger)
	}

	if err := d.usage.record(sess.SessionID, task.RunID, ModelUsage{Provider: "fallback", Model: "local", InputTokens: 9, Estimated: true}); err != nil {
		t.Fatal(err)
	}
	result, err = d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	ledger = result.(map[string]any)["ledger"].(map[string]any)
	if ledger["estimate_method"] != "chars/4" || ledger["estimated"] != true {
		t.Fatalf("estimated usage must fall back to chars/4, got %#v", ledger)
	}
	if ledger["model_visible_tokens_estimated"] != estimateTokens(tr.render()) {
		t.Fatalf("fallback tokens = %#v, want %d", ledger["model_visible_tokens_estimated"], estimateTokens(tr.render()))
	}
}

func TestInLoopCompactIsNotAdvertisedAsContextEngine(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	probe, err := d.handleContextDoctor(nil)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := probe.(map[string]any)
	reason, _ := doc["reason"].(string)
	if doc["engine"] != "noop" || doc["transformed"] != false {
		t.Fatalf("contextengine doctor must stay identity noop: %#v", doc)
	}
	if !strings.Contains(reason, "Transcript.compact") || strings.Contains(reason, "is a context engine") {
		t.Fatalf("doctor must not call compact a context engine: %q", reason)
	}

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	tr := newTranscript("hi")
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read f", Obs: Observation{Content: "x"}})
	if err := d.runs.saveCheckpointChecked(task.RunID, &runCheckpoint{Turn: 1, Transcript: tr}); err != nil {
		t.Fatal(err)
	}
	summary, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	compact := summary.(map[string]any)["compact"].(map[string]any)
	method, _ := compact["method"].(string)
	if strings.Contains(method, "context engine") || strings.Contains(method, "contextengine") {
		t.Fatalf("idle compact must not be advertised as a context engine: %#v", compact)
	}
}
