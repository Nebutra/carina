package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/scheduler"
)

func TestOpenAIResponsesSendsNativeReasoningEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		if reasoning["effort"] != "high" {
			t.Fatalf("responses payload reasoning = %#v", body["reasoning"])
		}
		if _, legacy := body["reasoning_effort"]; legacy {
			t.Fatalf("responses payload used legacy top-level field: %#v", body)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"ok","usage":{"input_tokens":9,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-test", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{id: "openai", baseURL: srv.URL, defaultModel: "gpt-5", auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client()}, responses: true}
	resp, err := p.Complete(context.Background(), modelrouter.Request{Model: "gpt-5", Prompt: "hello", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.EffectiveReasoningEffort != "high" || resp.InputTokens != 5 || resp.CacheReadTokens != 4 {
		t.Fatalf("response observability = %+v", resp)
	}
}

func TestOpenRouterSendsUnifiedReasoningObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		if reasoning["effort"] != "medium" {
			t.Fatalf("openrouter payload = %#v", body)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openrouter", "sk-test", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{id: "openrouter", baseURL: srv.URL, defaultModel: "openai/gpt-5", auth: auth.ProviderChain("openrouter", nil, store, nil), client: srv.Client()}}
	resp, err := p.Complete(context.Background(), modelrouter.Request{Prompt: "hello", ReasoningEffort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.EffectiveReasoningEffort != "medium" {
		t.Fatalf("effective effort = %+v", resp)
	}
}

func TestTaskSubmissionFreezesEffortInTaskFingerprintAndWAL(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	params := map[string]any{"session_id": sess.SessionID, "prompt": "reason carefully", "model": "openai/gpt-5", "reasoning_effort": "high", "client_submission_id": "effort-freeze"}
	result, err := d.handleTaskSubmit(mustJSON(t, params))
	if err != nil {
		t.Fatal(err)
	}
	task := result.(*scheduler.ExecutionRun)
	if task.RequestedReasoningEffort != "high" || task.EffectiveReasoningEffort != "high" {
		t.Fatalf("effort not frozen into task: %+v", task)
	}
	params["reasoning_effort"] = "low"
	if _, err := d.handleTaskSubmit(mustJSON(t, params)); err == nil {
		t.Fatal("idempotency fingerprint ignored changed reasoning effort")
	}
	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"requested_reasoning_effort":"high"`) || !strings.Contains(string(raw), `"effective_reasoning_effort":"high"`) {
		t.Fatalf("write-ahead event omitted effort: %s", raw)
	}
}

func TestAnthropicSendsAdaptiveThinkingEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		thinking, _ := body["thinking"].(map[string]any)
		output, _ := body["output_config"].(map[string]any)
		if thinking["type"] != "adaptive" || output["effort"] != "max" {
			t.Fatalf("anthropic payload = %#v", body)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-test", nil); err != nil {
		t.Fatal(err)
	}
	p := newAnthropicCatalogProvider("anthropic", "anthropic", srv.URL, "claude-sonnet-4-6", auth.ProviderChain("anthropic", nil, store, nil), nil, nil, nil)
	resp, err := p.Complete(context.Background(), modelrouter.Request{Prompt: "hello", ReasoningEffort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.EffectiveReasoningEffort != "max" || resp.CacheReadTokens != 1 {
		t.Fatalf("response observability = %+v", resp)
	}
}

func TestGemini3SendsNativeThinkingLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		config, _ := body["generationConfig"].(map[string]any)
		thinking, _ := config["thinkingConfig"].(map[string]any)
		if thinking["thinkingLevel"] != "LOW" || config["maxOutputTokens"] != float64(agentMaxOutputTokens) {
			t.Fatalf("gemini payload = %#v", body)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("google", "key-test", nil); err != nil {
		t.Fatal(err)
	}
	p := &geminiProvider{providerBase: providerBase{id: "google", baseURL: srv.URL, defaultModel: "gemini-3-pro", auth: auth.ProviderChain("google", nil, store, nil), client: srv.Client()}}
	resp, err := p.Complete(context.Background(), modelrouter.Request{Prompt: "hello", ReasoningEffort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.EffectiveReasoningEffort != "low" {
		t.Fatalf("effective effort = %+v", resp)
	}
}

func TestReasoningEffortRejectsUnsupportedModelsBeforeNetwork(t *testing.T) {
	body := map[string]any{}
	if _, err := applyNativeReasoningEffort("requesty", "gpt-5", "high", body); err == nil {
		t.Fatal("generic OpenAI-compatible provider silently accepted effort")
	}
	if _, err := applyNativeReasoningEffort("google", "gemini-2.5-pro", "high", body); err == nil {
		t.Fatal("Gemini 2.5 thinking budget was misrepresented as effort")
	}
	if _, err := validateReasoningEffort(catalogReasoningEffortSpec("anthropic", "claude-sonnet-4-5", provider.Model{Reasoning: true}), "high"); err == nil {
		t.Fatal("legacy Claude thinking model silently accepted adaptive effort")
	}
}

func TestCCSwitchCodexAndAzureEncodeOpenAIReasoningEffort(t *testing.T) {
	// Inventory maps ccswitch-codex-* / azure onto the OpenAI effort family; the
	// wire encoder must follow that family or Mox/TDS routes fail after Tab-select.
	for _, providerID := range []string{
		"ccswitch-codex-e29abfcda9ba",
		"azure",
		"azure-openai",
		"openrouter",
		"xai",
	} {
		body := map[string]any{}
		effective, err := applyNativeReasoningEffort(providerID, "gpt-5.6-sol", "low", body)
		if err != nil {
			t.Fatalf("%s: apply effort: %v", providerID, err)
		}
		if effective != "low" {
			t.Fatalf("%s: effective = %q", providerID, effective)
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		if reasoning["effort"] != "low" {
			t.Fatalf("%s: body reasoning = %#v", providerID, body["reasoning"])
		}
	}
}

func TestCCSwitchGeminiEncodesThinkingLevel(t *testing.T) {
	body := map[string]any{}
	effective, err := applyNativeReasoningEffort("ccswitch-gemini-deadbeef", "gemini-3-pro", "medium", body)
	if err != nil {
		t.Fatal(err)
	}
	if effective != "medium" {
		t.Fatalf("effective = %q", effective)
	}
	config, _ := body["generationConfig"].(map[string]any)
	thinking, _ := config["thinkingConfig"].(map[string]any)
	if thinking["thinkingLevel"] != "MEDIUM" {
		t.Fatalf("gemini family body = %#v", body)
	}
}

func TestCCSwitchCodexProviderSendsEffortOnResponsesWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		if reasoning["effort"] != "low" {
			t.Fatalf("ccswitch-codex responses payload = %#v", body["reasoning"])
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"ok","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	const providerID = "ccswitch-codex-e29abfcda9ba"
	if err := store.SetAPIKey(providerID, "sk-test", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: providerID, baseURL: srv.URL, defaultModel: "gpt-5.6-sol",
		auth: auth.ProviderChain(providerID, nil, store, nil), client: srv.Client(),
	}, responses: true}
	resp, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "gpt-5.6-sol", Prompt: "hi", ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.EffectiveReasoningEffort != "low" {
		t.Fatalf("effective effort = %+v", resp)
	}
}

func TestCatalogEffortOptionsDriveInventoryDefaults(t *testing.T) {
	model := provider.Model{Reasoning: true, ReasoningOptions: []json.RawMessage{json.RawMessage(`{"type":"effort","values":["minimal","low","high"]}`)}}
	spec := catalogReasoningEffortSpec("openai", "gpt-5", model)
	// Intersected with native OpenAI ladder; default prefers medium/high/low then first.
	if len(spec.Options) != 3 || spec.Options[0] != "minimal" || spec.Options[2] != "high" {
		t.Fatalf("catalog effort spec options = %#v", spec)
	}
	if spec.Default != "high" && spec.Default != "low" && spec.Default != "minimal" {
		t.Fatalf("catalog effort default = %#v", spec.Default)
	}
}

func TestCatalogEffortWithoutNativeFamilyUsesCatalog(t *testing.T) {
	// Unknown provider family: catalog-declared efforts still surface to inventory.
	model := provider.Model{
		Reasoning: true,
		ReasoningOptions: []json.RawMessage{
			json.RawMessage(`{"type":"effort","values":["low","turbo","ultra"]}`),
		},
	}
	spec := catalogReasoningEffortSpec("mox", "gpt-5.6-sol", model)
	if len(spec.Options) != 3 || spec.Options[1] != "turbo" || spec.Default != "low" {
		t.Fatalf("catalog-only effort spec = %#v", spec)
	}
}

func TestResolveEffortForModelKeepsExactThenRemapsThenDefaults(t *testing.T) {
	spec := reasoningEffortSpec{Options: []string{"low", "medium", "high", "max"}, Default: "high"}
	if got := resolveEffortForModel(spec, "medium"); got != "medium" {
		t.Fatalf("exact keep = %q", got)
	}
	if got := resolveEffortForModel(spec, "xhigh"); got != "max" {
		t.Fatalf("xhigh remap = %q", got)
	}
	if got := resolveEffortForModel(spec, "minimal"); got != "low" {
		t.Fatalf("minimal remap = %q", got)
	}
	if got := resolveEffortForModel(spec, "nope"); got != "high" {
		t.Fatalf("fallback default = %q", got)
	}
	empty := resolveEffortForModel(reasoningEffortSpec{}, "high")
	if empty != "" {
		t.Fatalf("no options should clear effort, got %q", empty)
	}
}

func TestReasoningEffortOverrideEnv(t *testing.T) {
	t.Setenv("CARINA_REASONING_EFFORT_OVERRIDES", `{"mox/gpt-5.6-sol":{"options":["low","deep"],"default":"deep"}}`)
	resetReasoningEffortOverridesForTest()
	defer resetReasoningEffortOverridesForTest()

	spec := catalogReasoningEffortSpec("mox", "gpt-5.6-sol", provider.Model{Reasoning: true})
	if len(spec.Options) != 2 || spec.Default != "deep" {
		t.Fatalf("override spec = %#v", spec)
	}
}

func TestCCSwitchClaudeLiveModelsExposeAdaptiveEffort(t *testing.T) {
	// Live-list stubs only set Reasoning:true; effort UI must still resolve from
	// protocol family (ccswitch-claude-*) + model markers (opus-5).
	spec := catalogReasoningEffortSpec(
		"ccswitch-claude-0c171e46f7b8",
		"claude-opus-5",
		provider.Model{ID: "claude-opus-5", Name: "claude-opus-5", Reasoning: true, ToolCall: true},
	)
	if len(spec.Options) < 3 || spec.Default == "" {
		t.Fatalf("CC Switch Claude adaptive effort missing: %#v", spec)
	}
	for _, want := range []string{"low", "medium", "high"} {
		found := false
		for _, got := range spec.Options {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("options %#v missing %q", spec.Options, want)
		}
	}
}

func TestCCSwitchCodexLiveModelsExposeOpenAIEffortLadder(t *testing.T) {
	spec := catalogReasoningEffortSpec(
		"ccswitch-codex-b184b08b0aa6",
		"gpt-5.5",
		provider.Model{ID: "gpt-5.5", Name: "gpt-5.5", Reasoning: true, ToolCall: true},
	)
	if len(spec.Options) < 4 || spec.Default == "" {
		t.Fatalf("CC Switch Codex effort ladder missing: %#v", spec)
	}
}
