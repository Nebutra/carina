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
	d.sched.AddTokens(task.RunID, 37)
	tr := newTranscript("inspect context")
	tr.Summary = "persisted summary"
	tr.addTurn(Turn{Thought: "inspect", Tool: "read", Obs: Observation{Content: "exact checkpoint content"}})
	cp := &runCheckpoint{Turn: 3, Transcript: tr, MemorySnapshot: "memory"}
	if err := d.runs.saveCheckpointChecked(task.RunID, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sched.RestoreCheckpoint(task.RunID, nil); err != nil {
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
	if taskSummary["mode"] != task.Mode {
		t.Fatalf("task execution mode = %#v, want %q", taskSummary["mode"], task.Mode)
	}
	ledger := out["ledger"].(map[string]any)
	if ledger["model_visible"] != tr.render() {
		t.Fatalf("ledger model_visible must equal tr.render()\n got %#v\nwant %#v", ledger["model_visible"], tr.render())
	}
	if ledger["model_visible_bytes"] != len(tr.render()) {
		t.Fatalf("ledger model_visible_bytes = %#v, want %d", ledger["model_visible_bytes"], len(tr.render()))
	}
	if method, _ := ledger["estimate_method"].(string); method != "chars/4" {
		t.Fatalf("token estimate method missing: %#v", ledger)
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
	if err := d.usage.record(sess.SessionID, task.RunID, ModelUsage{Provider: "openai", Model: "gpt-5", InputTokens: 700, CacheReadTokens: 150, OutputTokens: 20}); err != nil {
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
	breakdown := context["breakdown"].(map[string]any)
	if breakdown["input_tokens"] != 700 || breakdown["output_tokens"] != 20 || breakdown["cache_read_tokens"] != 150 {
		t.Fatalf("measured provider usage breakdown lost precision: %#v", breakdown)
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
	if err := d.usage.record(sess.SessionID, task.RunID, ModelUsage{Provider: "fallback", Model: "local", InputTokens: 42, Estimated: true}); err != nil {
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
	ledger := out["ledger"].(map[string]any)
	if ledger["available"].(bool) {
		t.Fatalf("empty session invented a model-visible ledger: %#v", ledger)
	}
}

func TestContextSummaryLedgerMatchesRenderedTranscriptAfterElide(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect context")
	tr := newTranscript("inspect context")
	tr.addTurn(Turn{Thought: "read", Tool: "read", ActionBrief: "read a.go", Path: "a.go", Obs: Observation{Content: "package a"}})
	tr.addTurn(Turn{Thought: "steer", Tool: "user", ActionBrief: "don't use X", Obs: Observation{Content: "don't use X"}})
	tr.addTurn(Turn{Thought: "pin", Tool: "read", ActionBrief: "read fail", Path: "fail.go", Obs: Observation{Content: "FAIL", Pinned: true}})
	tr.Turns[0].Obs.Elided = true
	tr.Turns[0].Obs.OriginalSHA256 = "deadbeef"
	tr.CompactionReceipts = []CompactionReceipt{{
		Version: 3, Mode: compactionModeCollapseOnly,
		Transforms:      []string{"elide_tool_output", "collapse_action_skeleton"},
		KeptTurnIndices: []int{tr.Turns[1].Index},
		RemovedTurns:    1,
	}}
	if err := d.runs.saveCheckpointChecked(task.RunID, &runCheckpoint{Turn: 3, Transcript: tr}); err != nil {
		t.Fatal(err)
	}

	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	ledger := result.(map[string]any)["ledger"].(map[string]any)
	if ledger["model_visible"] != tr.render() {
		t.Fatalf("model_visible != tr.render()\n got %q\nwant %q", ledger["model_visible"], tr.render())
	}
	if !strings.Contains(ledger["model_visible"].(string), "don't use X") {
		t.Fatalf("user constraint missing from model-visible ledger: %q", ledger["model_visible"])
	}
	if !strings.Contains(ledger["model_visible"].(string), "[elided to save context]") {
		t.Fatalf("elided turn not projected: %q", ledger["model_visible"])
	}
	elided := intSlice(t, ledger["elided_turns"])
	if len(elided) != 1 || elided[0] != tr.Turns[0].Index {
		t.Fatalf("elided_turns = %#v, want [%d]", ledger["elided_turns"], tr.Turns[0].Index)
	}
	pinned := intSlice(t, ledger["pinned_turns"])
	if len(pinned) != 1 || pinned[0] != tr.Turns[2].Index {
		t.Fatalf("pinned_turns = %#v, want [%d]", ledger["pinned_turns"], tr.Turns[2].Index)
	}
	receipts, ok := ledger["receipts"].([]CompactionReceipt)
	if !ok || len(receipts) != 1 || receipts[0].Mode != compactionModeCollapseOnly {
		t.Fatalf("ledger receipts missing collapse receipt: %#v", ledger["receipts"])
	}
	if len(receipts[0].KeptTurnIndices) != 1 || receipts[0].KeptTurnIndices[0] != tr.Turns[1].Index {
		t.Fatalf("kept user turn missing from receipt: %#v", receipts[0].KeptTurnIndices)
	}
	if ledger["summarizer_failures"] != 0 || ledger["summarizer_circuit"] != "closed" {
		t.Fatalf("idle ledger must report a closed summarizer circuit: %#v", ledger)
	}
}

func TestContextSummaryLedgerReportsOpenSummarizerCircuit(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "inspect context")
	tr := newTranscript("inspect context")
	tr.addTurn(Turn{Thought: "steer", Tool: "user", ActionBrief: "don't use X", Obs: Observation{Content: "don't use X", Pinned: true}})
	tr.SummarizerFailures = compactionFailureThreshold
	tr.CompactionReceipts = []CompactionReceipt{{
		Version: 3, Mode: compactionModeCollapseOnly,
		Transforms:         []string{"elide_tool_output", "collapse_action_skeleton", "summarizer_circuit_open"},
		SummarizerFailures: compactionFailureThreshold,
	}}
	if err := d.runs.saveCheckpointChecked(task.RunID, &runCheckpoint{Turn: 1, Transcript: tr}); err != nil {
		t.Fatal(err)
	}
	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	ledger := result.(map[string]any)["ledger"].(map[string]any)
	if ledger["summarizer_failures"] != compactionFailureThreshold || ledger["summarizer_circuit"] != "open" {
		t.Fatalf("open circuit missing from ledger: %#v", ledger)
	}
	if !strings.Contains(ledger["model_visible"].(string), "don't use X") {
		t.Fatalf("user constraint missing from model-visible ledger: %q", ledger["model_visible"])
	}
}

func TestPromptCacheKindIsAnthropicOnlyForAnthropicProtocol(t *testing.T) {
	catalog := provider.Seed()
	cases := []struct {
		model string
		want  string
	}{
		{"", "none"},
		{"default", "none"},
		{"grok-build/grok-4.6", "none"},
		{"openai/gpt-5", "none"},
		{"openrouter/anthropic/claude-sonnet-4-5", "none"},
		{"anthropic/claude-sonnet-4-5-20250929", "anthropic"},
		{"anthropic/claude", "anthropic"},
	}
	for _, tc := range cases {
		if got := promptCacheKindFor(catalog, nil, tc.model); got != tc.want {
			t.Fatalf("promptCacheKindFor(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
	relay := provider.Catalog{"relay": {ID: "relay", APIProtocol: "anthropic"}}
	if got := promptCacheKindFor(relay, nil, "relay/claude"); got != "anthropic" {
		t.Fatalf("catalog anthropic protocol = %q", got)
	}
	d := &Daemon{reasoner: &scriptedReasoner{steps: []string{`{"tool":"done"}`}}, providerCatalog: catalog}
	if got := d.promptCacheKind("openai/gpt-5"); got != "none" {
		t.Fatalf("reasoner presence must not invent anthropic cache, got %q", got)
	}
	if got := d.promptCacheKind("anthropic/claude-sonnet-4-5-20250929"); got != "anthropic" {
		t.Fatalf("anthropic route = %q", got)
	}
}

func TestContextSummaryLedgerLabelsOpenAICacheNone(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalAndModel(sess.SessionID, sess.WorkspaceID, "inspect context", "openai/gpt-5", nil)
	tr := newTranscript("inspect context")
	tr.addTurn(Turn{Thought: "go", Tool: "read", ActionBrief: "read x", Obs: Observation{Content: "ok"}})
	if err := d.runs.saveCheckpointChecked(task.RunID, &runCheckpoint{Turn: 1, Transcript: tr}); err != nil {
		t.Fatal(err)
	}
	if err := d.usage.record(sess.SessionID, task.RunID, ModelUsage{Provider: "openai", Model: "gpt-5", InputTokens: 20, OutputTokens: 4}); err != nil {
		t.Fatal(err)
	}

	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	ledger := result.(map[string]any)["ledger"].(map[string]any)
	if ledger["cache"] != "none" {
		t.Fatalf("OpenAI route must label cache=none: %#v", ledger)
	}
	for _, raw := range ledgerLayers(t, ledger["layers"]) {
		if raw["cache"] != "none" {
			t.Fatalf("OpenAI layer must be cache=none: %#v", raw)
		}
	}
	context := result.(map[string]any)["model_context_tokens"].(map[string]any)
	breakdown := context["breakdown"].(map[string]any)
	if breakdown["cache_read_tokens"] != 0 {
		t.Fatalf("OpenAI usage invented cache-read tokens: %#v", breakdown)
	}
}

func TestContextSummaryLedgerLabelsAnthropicCache(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalAndModel(sess.SessionID, sess.WorkspaceID, "inspect context", "anthropic/claude-sonnet-4-5-20250929", nil)
	tr := newTranscript("inspect context")
	tr.addTurn(Turn{Thought: "go", Tool: "read", ActionBrief: "read x", Obs: Observation{Content: "ok"}})
	if err := d.runs.saveCheckpointChecked(task.RunID, &runCheckpoint{Turn: 1, Transcript: tr}); err != nil {
		t.Fatal(err)
	}

	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	ledger := result.(map[string]any)["ledger"].(map[string]any)
	if ledger["cache"] != "anthropic" {
		t.Fatalf("Anthropic route must keep cache=anthropic: %#v", ledger)
	}
	foundIdentity, foundMode := false, false
	for _, raw := range ledgerLayers(t, ledger["layers"]) {
		switch raw["id"] {
		case "identity":
			foundIdentity = true
			if raw["cache"] != "anthropic" {
				t.Fatalf("Anthropic identity must stay cacheable: %#v", raw)
			}
		case "mode":
			foundMode = true
			if raw["cache"] != "anthropic" {
				t.Fatalf("Anthropic mode must stay cacheable: %#v", raw)
			}
		case "transcript":
			if raw["cache"] != "none" {
				t.Fatalf("transcript layer is volatile: %#v", raw)
			}
		}
	}
	if !foundIdentity || !foundMode {
		t.Fatal("missing named constitution layers (mode/identity)")
	}
}

func TestContextSummaryLedgerLabelsGrokCacheNone(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalAndModel(sess.SessionID, sess.WorkspaceID, "inspect context", "grok-build/grok-4.6", nil)
	tr := newTranscript("inspect context")
	tr.addTurn(Turn{Thought: "go", Tool: "read", ActionBrief: "read x", Obs: Observation{Content: "ok"}})
	if err := d.runs.saveCheckpointChecked(task.RunID, &runCheckpoint{Turn: 1, Transcript: tr}); err != nil {
		t.Fatal(err)
	}

	result, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	ledger := result.(map[string]any)["ledger"].(map[string]any)
	if ledger["cache"] != "none" {
		t.Fatalf("Grok route must label cache=none: %#v", ledger)
	}
	for _, raw := range ledgerLayers(t, ledger["layers"]) {
		if (raw["id"] == "identity" || raw["id"] == "mode" || raw["id"] == "protocol" || raw["id"] == "tools") && raw["cache"] != "none" {
			t.Fatalf("Grok constitution layer must be cache=none: %#v", raw)
		}
		if raw["id"] == "transcript" && raw["cache"] != "none" {
			t.Fatalf("transcript layer is volatile: %#v", raw)
		}
	}
}

func ledgerLayers(t *testing.T, raw any) []map[string]any {
	t.Helper()
	switch layers := raw.(type) {
	case []map[string]any:
		return layers
	case []any:
		out := make([]map[string]any, 0, len(layers))
		for _, layer := range layers {
			item, ok := layer.(map[string]any)
			if !ok {
				t.Fatalf("layer is not a map: %#v", layer)
			}
			out = append(out, item)
		}
		return out
	default:
		t.Fatalf("layers is not a slice: %#v", raw)
		return nil
	}
}

func intSlice(t *testing.T, raw any) []int {
	t.Helper()
	switch values := raw.(type) {
	case []int:
		return values
	case []any:
		out := make([]int, 0, len(values))
		for _, value := range values {
			switch n := value.(type) {
			case int:
				out = append(out, n)
			case float64:
				out = append(out, int(n))
			default:
				t.Fatalf("not an int: %#v", value)
			}
		}
		return out
	default:
		t.Fatalf("not a slice: %#v", raw)
		return nil
	}
}
