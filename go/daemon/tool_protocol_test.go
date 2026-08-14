package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

func TestOpenAIChatProviderDecodesNativeToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var haveRead bool
		for _, tool := range body.Tools {
			if tool.Function.Name == "read" {
				haveRead = true
				break
			}
		}
		if !haveRead {
			t.Fatalf("tools not sent: %+v", body.Tools)
		}
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"id":"c1","function":{"name":"read","arguments":"{\"path\":\"main.go\",\"intent\":\"inspect\"}"}}]}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openrouter", "sk-chat", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openrouter", baseURL: srv.URL + "/v1", defaultModel: "default-model",
		auth: auth.ProviderChain("openrouter", nil, store, nil), client: srv.Client(),
	}}
	resp, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "mixtral", Prompt: "hello", Tools: carinaToolSpecs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	act, err := decodeNativeToolCalls(resp.ToolCalls)
	if err != nil || act.Path != "main.go" {
		t.Fatalf("decoded = %+v err=%v", act, err)
	}
}

func TestGeminiProviderDecodesNativeFunctionCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "functionDeclarations") {
			t.Fatalf("gemini tools missing: %s", raw)
		}
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"a.go","intent":"inspect"}}}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3}}`)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("google", "gk", nil); err != nil {
		t.Fatal(err)
	}
	p := &geminiProvider{providerBase: providerBase{
		id: "google", baseURL: srv.URL, defaultModel: "gemini-3-pro",
		auth: auth.ProviderChain("google", nil, store, nil), client: srv.Client(),
	}}
	resp, err := p.Complete(context.Background(), modelrouter.Request{
		Prompt: "hello", Tools: carinaToolSpecs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	act, err := decodeNativeToolCalls(resp.ToolCalls)
	if err != nil || act.Tool != "read" || act.Path != "a.go" {
		t.Fatalf("decoded = %+v err=%v resp=%+v", act, err, resp)
	}
}

func TestProviderToolsUnsupportedIsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"tools is unsupported for this model"}}`)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openrouter", "sk-chat", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openrouter", baseURL: srv.URL + "/v1", defaultModel: "default-model",
		auth: auth.ProviderChain("openrouter", nil, store, nil), client: srv.Client(),
	}}
	_, err := p.Complete(context.Background(), modelrouter.Request{
		Prompt: "hello", Tools: carinaToolSpecs(),
	})
	if !toolsUnsupported(err) {
		t.Fatalf("want tools unsupported, got %v", err)
	}
}

func TestNativeToolsIneligibleWithoutCatalogEvidence(t *testing.T) {
	d := &Daemon{nativeToolsHTTP: true, providerCatalog: provider.Catalog{}}
	if d.nativeToolsEligible(&routerReasoner{}, "unknown/model") {
		t.Fatal("unknown model must stay JSON-only")
	}
}

func TestNativeToolsIneligibleForGrokBuildPrefix(t *testing.T) {
	d := &Daemon{
		nativeToolsHTTP: true,
		providerCatalog: provider.Catalog{
			"grok-build": {ID: "grok-build", Models: map[string]provider.Model{
				"grok-4.6": {ID: "grok-4.6", ToolCall: true},
			}},
		},
	}
	if d.nativeToolsEligible(&routerReasoner{}, "grok-build/grok-4.6") {
		t.Fatal("dedicated Grok Build routes must stay off the HTTP native-tool path")
	}
}

func TestOpenAIResponsesProviderDecodesNativeToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var haveRead bool
		for _, tool := range body.Tools {
			if tool.Name == "read" && tool.Type == "function" {
				haveRead = true
				break
			}
		}
		if !haveRead {
			t.Fatalf("responses tools not flattened: %+v", body.Tools)
		}
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"output":[{"type":"function_call","call_id":"c1","name":"read","arguments":"{\"path\":\"main.go\",\"intent\":\"inspect\"}"}],"usage":{"input_tokens":2,"output_tokens":3}}`)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-resp", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL + "/v1", defaultModel: "default-model",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}, responses: true}
	resp, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "gpt", Prompt: "hello", Tools: carinaToolSpecs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	act, err := decodeNativeToolCalls(resp.ToolCalls)
	if err != nil || act.Tool != "read" || act.Path != "main.go" {
		t.Fatalf("decoded = %+v err=%v resp=%+v", act, err, resp)
	}
}

func TestOpenAIChatStreamDecodesNativeToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["tools"] == nil {
			t.Fatalf("stream tools missing: %+v", body)
		}
		writeSSE(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read","arguments":"{\"path\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\",\"intent\":\"inspect\"}"}}]}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`,
			`[DONE]`,
		)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-stream", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL + "/v1", defaultModel: "gpt-5",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}}
	resp, err := p.Stream(context.Background(), modelrouter.Request{
		Prompt: "hello", Tools: carinaToolSpecs(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	act, err := decodeNativeToolCalls(resp.ToolCalls)
	if err != nil || act.Path != "a.go" {
		t.Fatalf("decoded = %+v err=%v resp=%+v", act, err, resp)
	}
}

func TestOpenAIResponsesStreamDecodesNativeToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"name":"read"`) {
			t.Fatalf("responses stream tools missing: %s", raw)
		}
		writeSSE(w,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc1","call_id":"c1","name":"read","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"a.go\",\"intent\":\"inspect\"}"}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"path\":\"a.go\",\"intent\":\"inspect\"}"}`,
			`{"type":"response.completed","response":{"output":[{"type":"function_call","name":"read","call_id":"c1","arguments":"{\"path\":\"a.go\",\"intent\":\"inspect\"}"}],"usage":{"input_tokens":4,"output_tokens":2}}}`,
		)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-resp-stream", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL + "/v1", defaultModel: "gpt-5",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}, responses: true}
	resp, err := p.Stream(context.Background(), modelrouter.Request{
		Prompt: "hello", Tools: carinaToolSpecs(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	act, err := decodeNativeToolCalls(resp.ToolCalls)
	if err != nil || act.Path != "a.go" {
		t.Fatalf("decoded = %+v err=%v resp=%+v", act, err, resp)
	}
}

func TestAnthropicStreamDecodesNativeToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"input_schema"`) {
			t.Fatalf("anthropic tools missing: %s", raw)
		}
		writeSSE(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":4,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu1","name":"read","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":",\"intent\":\"inspect\"}"}}`,
			`{"type":"message_delta","usage":{"output_tokens":3}}`,
		)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client()}
	resp, err := p.Stream(context.Background(), modelrouter.Request{
		Prompt: "hello", Tools: carinaToolSpecs(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	act, err := decodeNativeToolCalls(resp.ToolCalls)
	if err != nil || act.Path != "a.go" {
		t.Fatalf("decoded = %+v err=%v resp=%+v", act, err, resp)
	}
}
