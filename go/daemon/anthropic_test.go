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
)

func TestAnthropicProviderUsesStablePrefixCacheControlAndParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("Anthropic request path = %q", r.URL.Path)
		}
		var body struct {
			MaxTokens int `json:"max_tokens"`
			System    []struct {
				Type         string            `json:"type"`
				Text         string            `json:"text"`
				CacheControl map[string]string `json:"cache_control"`
			} `json:"system"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type         string            `json:"type"`
					Text         string            `json:"text"`
					CacheControl map[string]string `json:"cache_control"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.MaxTokens != agentMaxOutputTokens {
			t.Fatalf("max_tokens = %d, want %d", body.MaxTokens, agentMaxOutputTokens)
		}
		if len(body.System) != 1 || body.System[0].Text != "stable" || body.System[0].CacheControl["type"] != "ephemeral" {
			t.Fatalf("system = %+v", body.System)
		}
		if len(body.Messages) != 1 || len(body.Messages[0].Content) != 1 {
			t.Fatalf("messages = %+v", body.Messages)
		}
		suffix := body.Messages[0].Content[0]
		if suffix.Text != "volatile" || len(suffix.CacheControl) != 0 {
			t.Fatalf("volatile suffix should not be cacheable: %+v", suffix)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":5,"cache_read_input_tokens":7}}`))
	}))
	defer srv.Close()

	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client(),
	}
	resp, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "default", Prompt: "stablevolatile", StablePrefix: "stable", VolatileSuffix: "volatile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.InputTokens != 2 || resp.OutputTokens != 3 || resp.CacheWriteTokens != 5 || resp.CacheReadTokens != 7 {
		t.Fatalf("usage = %+v", resp)
	}
}

func TestAnthropicProviderCachesEachPromptSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []struct {
				Type         string            `json:"type"`
				Text         string            `json:"text"`
				CacheControl map[string]string `json:"cache_control"`
			} `json:"system"`
			Messages []struct {
				Content []struct {
					Type         string            `json:"type"`
					Text         string            `json:"text"`
					CacheControl map[string]string `json:"cache_control"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.System) != 3 {
			t.Fatalf("want constitution + workspace + catalog on system, got %+v", body.System)
		}
		if body.System[0].Text != "CONSTITUTION" || body.System[0].CacheControl["type"] != "ephemeral" {
			t.Fatalf("constitution system = %+v", body.System[0])
		}
		for i, name := range []string{"WORKSPACE", "CATALOG"} {
			if body.System[1+i].Text != name || len(body.System[1+i].CacheControl) != 0 {
				t.Fatalf("dynamic system %d = %+v", i, body.System[1+i])
			}
		}
		blocks := body.Messages[0].Content
		if len(blocks) != 1 {
			t.Fatalf("user must be volatile only, got %+v", blocks)
		}
		if !strings.Contains(blocks[0].Text, "TASK: x") || !strings.Contains(blocks[0].Text, "turn") || len(blocks[0].CacheControl) != 0 {
			t.Fatalf("volatile = %+v", blocks[0])
		}
		if strings.Contains(blocks[0].Text, "CONSTITUTION") || strings.Contains(blocks[0].Text, "You are Carina") {
			t.Fatalf("constitution leaked into user: %+v", blocks[0])
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client(),
	}
	seg := buildPromptSegmentsFromLayers(promptLayers{
		Constitution: "CONSTITUTION",
		Workspace:    "WORKSPACE",
		Catalog:      "CATALOG",
	}, "x", "turn", "GO")
	_, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "default", Prompt: seg.full(), StablePrefix: seg.StablePrefix,
		StableSections:  seg.CacheSections(),
		SystemSections:  seg.ConstitutionSections(),
		DynamicSections: seg.DynamicSections(),
		VolatileSuffix:  seg.VolatileSuffix,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicProviderCapsCacheControlAtFourBreakpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []struct {
				Type         string            `json:"type"`
				Text         string            `json:"text"`
				CacheControl map[string]string `json:"cache_control"`
			} `json:"system"`
			Messages []struct {
				Content []struct {
					Type         string            `json:"type"`
					Text         string            `json:"text"`
					CacheControl map[string]string `json:"cache_control"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.System) != 6 {
			t.Fatalf("want 4 cached A–D + 2 dynamic system, got %+v", body.System)
		}
		for i, name := range []string{"MODE", "IDENTITY", "PROTOCOL", "TOOLS"} {
			if body.System[i].Text != name || body.System[i].CacheControl["type"] != "ephemeral" {
				t.Fatalf("cached system %d = %+v", i, body.System[i])
			}
		}
		for i, name := range []string{"WORKSPACE", "CATALOG"} {
			block := body.System[4+i]
			if block.Text != name || len(block.CacheControl) != 0 {
				t.Fatalf("dynamic system %d = %+v", i, block)
			}
		}
		blocks := body.Messages[0].Content
		if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "TASK: x") || len(blocks[0].CacheControl) != 0 {
			t.Fatalf("volatile = %+v", blocks)
		}
		if strings.Contains(blocks[0].Text, "MODE") || strings.Contains(blocks[0].Text, "You are Carina") {
			t.Fatalf("constitution leaked into user: %+v", blocks[0])
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client(),
	}
	seg := buildPromptSegmentsFromLayers(promptLayers{
		Mode: "MODE", Identity: "IDENTITY", Protocol: "PROTOCOL", Tools: "TOOLS",
		Workspace: "WORKSPACE", Catalog: "CATALOG",
	}, "x", "turn", "GO")
	if len(seg.CacheSections()) != 6 {
		t.Fatalf("cache sections = %#v", seg.CacheSections())
	}
	_, err := p.Complete(context.Background(), modelrouter.Request{
		Model: "default", Prompt: seg.full(), StablePrefix: seg.StablePrefix,
		StableSections:  seg.CacheSections(),
		SystemSections:  seg.ConstitutionSections(),
		DynamicSections: seg.DynamicSections(),
		VolatileSuffix:  seg.VolatileSuffix,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicGreetingUserOmitsConstitution(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "hi")
	layers := d.composeAgentPromptLayers(sess, task, "")
	seg := buildPromptSegmentsFromLayers(layers, task.UserPrompt, "", "GO")
	req := modelrouter.Request{
		Prompt:          seg.full(),
		StablePrefix:    seg.StablePrefix,
		SystemSections:  seg.ConstitutionSections(),
		DynamicSections: seg.DynamicSections(),
		VolatileSuffix:  seg.VolatileSuffix,
	}
	system := anthropicSystemBlocks(req)
	if len(system) == 0 {
		t.Fatal("greeting must still send constitution on system")
	}
	joinedSystem := ""
	for _, block := range system {
		text, _ := block["text"].(string)
		joinedSystem += text
	}
	if !strings.Contains(joinedSystem, "You are Carina") {
		t.Fatalf("identity missing from system: %q", truncate(joinedSystem, 200))
	}
	user, ok := anthropicUserBlocks(req)
	if !ok || len(user) != 1 {
		t.Fatalf("greeting user = %+v", user)
	}
	text, _ := user[0]["text"].(string)
	if !strings.Contains(text, "TASK: hi") {
		t.Fatalf("greeting user missing TASK: %q", text)
	}
	if strings.Contains(text, "You are Carina") || strings.Contains(text, "Harness protocol:") {
		t.Fatalf("greeting user carried constitution: %q", truncate(text, 240))
	}
	if _, cached := user[0]["cache_control"]; cached {
		t.Fatal("TASK must not carry cache_control")
	}
}

func TestAnthropicEndpointNormalizesVersionExactlyOnce(t *testing.T) {
	tests := map[string]string{
		"https://api.example":                    "https://api.example/v1/messages",
		"https://api.example/":                   "https://api.example/v1/messages",
		"https://api.example/v1":                 "https://api.example/v1/messages",
		"https://api.example/prefix":             "https://api.example/prefix/v1/messages",
		"https://api.example/prefix/v1":          "https://api.example/prefix/v1/messages",
		"https://api.example/v1/messages":        "https://api.example/v1/messages",
		"https://api.example/prefix/v1/messages": "https://api.example/prefix/v1/messages",
	}
	for baseURL, want := range tests {
		if got := anthropicEndpoint(baseURL, "messages"); got != want {
			t.Errorf("anthropicEndpoint(%q) = %q, want %q", baseURL, got, want)
		}
	}
}

func TestAnthropicProviderClassifiesHTMLSuccessAsProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>control panel</body></html>"))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetAPIKey("anthropic", "sk-ant", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "anthropic", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("anthropic", nil, store, nil), client: srv.Client(),
	}
	_, err := p.Complete(context.Background(), modelrouter.Request{Prompt: "hello"})
	if err == nil || !strings.Contains(err.Error(), "HTML document, expected JSON") {
		t.Fatalf("HTML response error = %v", err)
	}
	info := classifyProviderError(err)
	if info.Code != "provider_protocol_error" || info.Retryable || info.UserAction == "" {
		t.Fatalf("HTML response classification = %+v", info)
	}
}

func TestAnthropicProviderUsesBearerCredentialFromImportedProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer imported-token" || r.Header.Get("x-api-key") != "" {
			t.Fatalf("request path=%q auth=%q api-key=%q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	store := testAuthStore(t)
	if err := store.SetBearerToken("relay", "imported-token", nil); err != nil {
		t.Fatal(err)
	}
	p := &anthropicProvider{
		id: "relay", baseURL: srv.URL, model: "claude-test",
		auth: auth.ProviderChain("relay", nil, store, nil), client: srv.Client(),
	}
	resp, err := p.Complete(context.Background(), modelrouter.Request{Prompt: "hello"})
	if err != nil || resp.Text != "OK" {
		t.Fatalf("bearer completion = %+v, err=%v", resp, err)
	}
}
