package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

func writeOpenAIChatSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("content-type", "text/event-stream")
	for _, event := range events {
		_, _ = w.Write([]byte("data: " + event + "\n\n"))
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func writeOpenAIChatSSEToolCall(w http.ResponseWriter, name, arguments string) {
	payload, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index": 0,
					"id":    "c1",
					"function": map[string]any{
						"name":      name,
						"arguments": arguments,
					},
				}},
			},
		}},
		"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3},
	})
	writeOpenAIChatSSE(w, string(payload))
}

func writeOpenAIChatSSEContent(w http.ResponseWriter, content string) {
	payload, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{"content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
	})
	writeOpenAIChatSSE(w, string(payload))
}

func requestHasTools(raw []byte) bool {
	var body struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	return len(body.Tools) > 0
}

func startNativeHTTPLoop(t *testing.T, d *Daemon, toolCall bool, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	catalog := provider.Catalog{
		"openrouter": {ID: "openrouter", Models: map[string]provider.Model{"model": {ID: "model", ToolCall: toolCall}}},
	}
	d.providerCatalog = catalog
	if d.authStore == nil {
		d.authStore = testAuthStore(t)
	}
	if err := d.authStore.SetAPIKey("openrouter", "sk", nil); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	router := modelrouter.New()
	router.RegisterProvider(&openAIProvider{providerBase: providerBase{
		id: "openrouter", baseURL: srv.URL + "/v1", defaultModel: "model",
		auth: auth.ProviderChain("openrouter", nil, d.authStore, nil), client: srv.Client(),
	}})
	d.SetReasoner(newRouterReasonerWithCatalog(router, "openrouter/model", catalog))
	return srv
}

func TestAgentLoopNativeDoneUsesBeginToolCallPath(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	var sawTools bool
	startNativeHTTPLoop(t, d, true, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if requestHasTools(raw) {
			sawTools = true
		}
		writeOpenAIChatSSEToolCall(w, "done", `{"summary":"Native done answer."}`)
	})

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalAndModel(sess.SessionID, sess.WorkspaceID, "say hi", "openrouter/model", nil)
	d.runTask(sess, task)
	got, _ := d.sched.Get(task.RunID)
	if got.Status != "completed" {
		t.Fatalf("status = %s result=%q", got.Status, got.Summary)
	}
	if !sawTools {
		t.Fatal("eligible HTTP route did not receive tools")
	}
	events, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	if err := json.Unmarshal(events, &evs); err != nil {
		t.Fatal(err)
	}
	var sawNative, sawPresentation bool
	for _, event := range evs {
		payload, _ := event["payload"].(map[string]any)
		switch event["type"] {
		case "RoutingOutcome":
			if payload["tool_protocol"] == "native" {
				sawNative = true
			}
		case "ModelResponded":
			if payload["presentation_text"] == "Native done answer." {
				sawPresentation = true
			}
		}
	}
	if !sawNative || !sawPresentation {
		t.Fatalf("native done not recorded: native=%v presentation=%v events=%v", sawNative, sawPresentation, evs)
	}
}

func TestAgentLoopNativeReadUsesBeginToolCall(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hello from native read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	startNativeHTTPLoop(t, d, true, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if calls == 0 && !requestHasTools(raw) {
			t.Fatal("first native turn must advertise tools")
		}
		calls++
		if calls == 1 {
			writeOpenAIChatSSEToolCall(w, "read", `{"path":"hello.txt","intent":"inspect the greeting"}`)
			return
		}
		writeOpenAIChatSSEToolCall(w, "done", `{"summary":"The file says hello from native read."}`)
	})

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalAndModel(sess.SessionID, sess.WorkspaceID, "read hello.txt", "openrouter/model", nil)
	d.runTask(sess, task)
	got, _ := d.sched.Get(task.RunID)
	if got.Status != "completed" {
		t.Fatalf("status = %s result=%q calls=%d", got.Status, got.Summary, calls)
	}
	events, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	if err := json.Unmarshal(events, &evs); err != nil {
		t.Fatal(err)
	}
	var sawRequested, sawStarted, sawFileRead, sawCompleted bool
	for _, event := range evs {
		payload, _ := event["payload"].(map[string]any)
		switch event["type"] {
		case "ToolCallRequested":
			if payload["tool"] == "read" {
				sawRequested = true
			}
		case "ToolCallStarted":
			if payload["tool"] == "read" {
				sawStarted = true
			}
		case "FileRead":
			sawFileRead = true
		case "ToolCallCompleted":
			if payload["tool"] == "read" {
				sawCompleted = true
			}
		}
	}
	if !sawRequested || !sawStarted || !sawFileRead || !sawCompleted {
		t.Fatalf("native read missed lifecycle events requested=%v started=%v file=%v completed=%v",
			sawRequested, sawStarted, sawFileRead, sawCompleted)
	}
}

func TestAgentLoopMalformedNativeFallsBackToJSON(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	calls := 0
	var sawFallback bool
	startNativeHTTPLoop(t, d, true, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			writeOpenAIChatSSEToolCall(w, "read", "not-json")
			return
		}
		if !requestHasTools(raw) && strings.Contains(string(raw), "not a valid action JSON") {
			sawFallback = true
		}
		writeOpenAIChatSSEContent(w, `{"tool":"done","summary":"JSON fallback worked."}`)
	})
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalAndModel(sess.SessionID, sess.WorkspaceID, "say hi", "openrouter/model", nil)
	d.runTask(sess, task)
	got, _ := d.sched.Get(task.RunID)
	if got.Status != "completed" || calls < 2 {
		t.Fatalf("status=%s result=%q calls=%d", got.Status, got.Summary, calls)
	}
	if !sawFallback {
		t.Fatal("JSON requery did not restore the cookbook path")
	}
	events, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	if err := json.Unmarshal(events, &evs); err != nil {
		t.Fatal(err)
	}
	var sawJSONFallback bool
	for _, event := range evs {
		if event["type"] != "RoutingOutcome" {
			continue
		}
		payload, _ := event["payload"].(map[string]any)
		if payload["tool_protocol"] == "json_fallback" {
			sawJSONFallback = true
		}
	}
	if !sawJSONFallback {
		t.Fatal("malformed native arguments must record tool_protocol=json_fallback")
	}
}

func TestAgentLoopToolsUnsupportedFallsBackToJSON(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	calls := 0
	startNativeHTTPLoop(t, d, true, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			if !requestHasTools(raw) {
				t.Fatal("eligible first turn must send tools")
			}
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"tools is unsupported for this model"}}`)
			return
		}
		if requestHasTools(raw) {
			t.Fatal("JSON fallback must not send tools")
		}
		writeOpenAIChatSSEContent(w, `{"tool":"done","summary":"JSON after tools rejection."}`)
	})
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalAndModel(sess.SessionID, sess.WorkspaceID, "say hi", "openrouter/model", nil)
	d.runTask(sess, task)
	got, _ := d.sched.Get(task.RunID)
	if got.Status != "completed" || calls < 2 {
		t.Fatalf("status=%s result=%q calls=%d", got.Status, got.Summary, calls)
	}
}

func TestAgentLoopSkipsToolsWhenCatalogSaysNo(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	var sawTools bool
	startNativeHTTPLoop(t, d, false, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if requestHasTools(raw) {
			sawTools = true
		}
		writeOpenAIChatSSEContent(w, `{"tool":"done","summary":"JSON only."}`)
	})
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.SubmitWithGoalAndModel(sess.SessionID, sess.WorkspaceID, "say hi", "openrouter/model", nil)
	d.runTask(sess, task)
	if sawTools {
		t.Fatal("ToolCall=false route must not receive tools")
	}
}
