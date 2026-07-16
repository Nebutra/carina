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
	task := result.(*scheduler.Task)
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
	p := newAnthropicCatalogProvider("anthropic", srv.URL, "claude-sonnet-4-6", auth.ProviderChain("anthropic", nil, store, nil), nil, nil, nil)
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
		if thinking["thinkingLevel"] != "LOW" {
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

func TestCatalogEffortOptionsDriveInventoryDefaults(t *testing.T) {
	model := provider.Model{Reasoning: true, ReasoningOptions: []json.RawMessage{json.RawMessage(`{"type":"effort","values":["minimal","low","high"]}`)}}
	spec := catalogReasoningEffortSpec("openai", "gpt-5", model)
	if len(spec.Options) != 3 || spec.Options[0] != "minimal" || spec.Options[2] != "high" || spec.Default != "minimal" {
		t.Fatalf("catalog effort spec = %#v", spec)
	}
}
