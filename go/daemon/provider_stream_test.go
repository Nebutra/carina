package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

func writeSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("content-type", "text/event-stream")
	for _, event := range events {
		_, _ = w.Write([]byte("data: " + event + "\n\n"))
	}
}

func TestOpenAIChatStreamPreservesDeltasUsageAndReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("accept") != "text/event-stream" {
			t.Fatalf("request path=%q accept=%q", r.URL.Path, r.Header.Get("accept"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		if body["stream"] != true || reasoning["effort"] != "high" {
			t.Fatalf("stream body = %+v", body)
		}
		writeSSE(w,
			`{"choices":[{"delta":{"content":"{\"tool\":\"done\","}}]}`,
			`{"choices":[{"delta":{"content":"\"summary\":\"ready\"}"}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":19,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":11}}}`,
			`[DONE]`,
		)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-test", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL + "/v1", defaultModel: "gpt-5",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}}
	var events []modelrouter.StreamEvent
	resp, err := p.Stream(context.Background(), modelrouter.Request{Prompt: "hello", ReasoningEffort: "high"}, func(event modelrouter.StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"tool":"done","summary":"ready"}` || resp.InputTokens != 8 || resp.CacheReadTokens != 11 || resp.OutputTokens != 7 || resp.EffectiveReasoningEffort != "high" {
		t.Fatalf("response = %+v", resp)
	}
	if got := events[0].Delta + events[1].Delta; got != resp.Text {
		t.Fatalf("deltas = %q", got)
	}
}

func TestOpenAIResponsesStreamPreservesUsageAndResetsBeforeChatFallback(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/responses":
			http.NotFound(w, r)
		case "/v1/chat/completions":
			writeSSE(w,
				`{"choices":[{"delta":{"content":"{\"tool\":\"done\",\"summary\":\"fallback\"}"}}]}`,
				`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2}}}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-test", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL + "/v1", defaultModel: "gpt-5",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}, responses: true}
	var events []modelrouter.StreamEvent
	resp, err := p.Stream(context.Background(), modelrouter.Request{Prompt: "hello"}, func(event modelrouter.StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"/v1/responses", "/v1/chat/completions"}) {
		t.Fatalf("paths = %v", paths)
	}
	if len(events) != 2 || !events[0].Reset || events[1].Delta != resp.Text || strings.Contains(events[1].Delta, "discard") {
		t.Fatalf("fallback events = %+v", events)
	}
	if resp.InputTokens != 3 || resp.CacheReadTokens != 2 || resp.OutputTokens != 3 {
		t.Fatalf("fallback usage = %+v", resp)
	}
}

func TestOpenAIResponsesStreamPreservesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w,
			`{"type":"response.output_text.delta","delta":"{\"tool\":\"done\","}`,
			`{"type":"response.output_text.delta","delta":"\"summary\":\"responses\"}"}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":23,"output_tokens":9,"input_tokens_details":{"cached_tokens":13}}}}`,
		)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-test", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL, defaultModel: "gpt-5",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}, responses: true}
	resp, err := p.Stream(context.Background(), modelrouter.Request{Prompt: "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.InputTokens != 10 || resp.CacheReadTokens != 13 || resp.OutputTokens != 9 {
		t.Fatalf("usage = %+v", resp)
	}
}

func TestAnthropicStreamIgnoresThinkingAndPreservesCacheUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		writeSSE(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":17,"output_tokens":1,"cache_creation_input_tokens":5,"cache_read_input_tokens":7}}}`,
			`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"private chain"}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"{\"tool\":\"done\","}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"\"summary\":\"anthropic\"}"}}`,
			`{"type":"message_delta","usage":{"output_tokens":8}}`,
		)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{id: "anthropic", baseURL: srv.URL, model: "claude-test", auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client()}
	var text strings.Builder
	resp, err := p.Stream(context.Background(), modelrouter.Request{Prompt: "hello"}, func(event modelrouter.StreamEvent) {
		text.WriteString(event.Delta)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text.String(), "private chain") || text.String() != resp.Text {
		t.Fatalf("public deltas = %q response=%q", text.String(), resp.Text)
	}
	if resp.InputTokens != 17 || resp.OutputTokens != 8 || resp.CacheWriteTokens != 5 || resp.CacheReadTokens != 7 {
		t.Fatalf("usage = %+v", resp)
	}
}

func TestStreamHTTPClientDoesNotWallClockBody(t *testing.T) {
	client := newProviderStreamHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("stream client Timeout = %s, want 0 (body must not be wall-clock capped)", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != providerStreamHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %+v", client.Transport)
	}
	// Injected complete-style clients with Timeout are replaced for streaming.
	replaced := streamHTTPClientOr(&http.Client{Timeout: providerHTTPTimeout})
	if replaced.Timeout != 0 {
		t.Fatalf("streamHTTPClientOr kept wall-clock Timeout %s", replaced.Timeout)
	}
	// httptest-style unbounded clients are preserved for tests.
	keep := &http.Client{Timeout: 0}
	if streamHTTPClientOr(keep) != keep {
		t.Fatal("streamHTTPClientOr should keep Timeout=0 injected clients")
	}
}

func TestStreamBodyBudgetErrorsAreNotAutoRetryable(t *testing.T) {
	cases := []error{
		providerStreamError{provider: "TDS", phase: "body", err: errors.New("context deadline exceeded (Client.Timeout or context cancellation while reading body)")},
		providerStreamError{provider: "TDS", phase: "idle", err: errProviderStreamIdle},
		wrapProviderStreamBodyError("TDS", errProviderStreamIdle),
		errors.New("ccswitch-codex-managed-proxy: TDS: read event stream: context deadline exceeded (Client.Timeout or context cancellation while reading body)"),
	}
	for _, err := range cases {
		info := classifyProviderError(err)
		if info.Retryable {
			t.Fatalf("expected non-retryable for %v, got %+v", err, info)
		}
		if info.Code != "provider_stream_budget_exceeded" && info.Code != "provider_stream_failed" {
			// legacy string path uses provider_stream_budget_exceeded
			if !strings.Contains(strings.ToLower(err.Error()), "while reading body") {
				t.Fatalf("code = %q for %v", info.Code, err)
			}
		}
		if info.UserAction == "" {
			t.Fatalf("missing user action for %v", err)
		}
	}
}

func TestStreamIdleTimeoutAbortsHungSSE(t *testing.T) {
	oldIdle := providerStreamIdleTimeout
	providerStreamIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { providerStreamIdleTimeout = oldIdle })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		// Hang without closing — idle reader should abort.
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("openai", "sk-test", nil); err != nil {
		t.Fatal(err)
	}
	p := &openAIProvider{providerBase: providerBase{
		id: "openai", baseURL: srv.URL + "/v1", defaultModel: "gpt-5",
		auth: auth.ProviderChain("openai", nil, store, nil), client: srv.Client(),
	}}
	_, err := p.Stream(context.Background(), modelrouter.Request{Prompt: "hello"}, nil)
	if err == nil {
		t.Fatal("expected idle timeout error")
	}
	info := classifyProviderError(err)
	if info.Retryable || info.Code != "provider_stream_budget_exceeded" {
		t.Fatalf("classification = %+v err=%v", info, err)
	}
	if !strings.Contains(err.Error(), "Not auto-retried") {
		t.Fatalf("error should explain no auto-retry: %v", err)
	}
}
