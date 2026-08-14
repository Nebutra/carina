package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

const grokACPFixturePromptUsage = `{"inputTokens":20,"outputTokens":7,"totalTokens":27,"cachedReadTokens":5,"cacheCreationTokens":3,"reasoningTokens":2,"modelCalls":1,"apiDurationMs":10,"numTurns":1}`

const grokACPFixturePromptMeta = `{"sessionId":"session-1","requestId":"carina-one-shot","promptId":"carina-one-shot","totalTokens":27,"modelId":"grok-4.6","inputTokens":20,"outputTokens":7,"cachedReadTokens":5,"reasoningTokens":2,"usage":` + grokACPFixturePromptUsage + `}`

const grokACPFixtureAgentCapabilities = `{"loadSession":true,"promptCapabilities":{"image":false,"audio":false,"embeddedContext":true},"mcpCapabilities":{"http":true,"sse":true},"sessionCapabilities":{"list":{},"resume":{},"close":{}},"auth":{},"_meta":{"x.ai/fs_notify":true,"x.ai/hooks":{"blockingEvents":["pre_tool_use","stop","subagent_stop"],"decisions":["deny","block"],"stopSignals":["continue","stopReason","additionalContext"]},"x.ai/capabilities":{"toolOverrides":{"x_keyword_search":true,"x_semantic_search":true,"x_user_search":false,"x_thread_fetch":false}}}}`

func TestGrokCLIArgsUseACPAndNeverExposePrompt(t *testing.T) {
	r := &grokCLIReasoner{}
	ctx := withReasoningEffort(context.Background(), "low")
	got := r.args(ctx, "grok-4.6")
	want := []string{"agent", "--no-leader", "--model", "grok-4.6", "--reasoning-effort", "low", "stdio"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	joined := strings.Join(got, " ")
	for _, forbidden := range []string{"-p", "--tools", "return JSON", "access_token", "refresh_token"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("ACP argument list contains %q: %s", forbidden, joined)
		}
	}
}

func TestGrokCLIEnvironmentIsOAuthOnlyAllowlist(t *testing.T) {
	env := grokCLIEnvironment([]string{
		"HOME=/real/home", "PATH=/usr/bin", "LANG=en_US.UTF-8",
		"XAI_API_KEY=secret", "GROK_CODE_XAI_API_KEY=secret", "GROK_DEPLOYMENT_KEY=secret", // gitleaks:allow -- synthetic rejection fixture
		"GROK_EXTRA_AUTH_KEY=secret", "GROK_AUTH=secret", "GROK_AGENT=unsafe",
		"GROK_CLI_CHAT_PROXY_BASE_URL=https://attacker.invalid", "HTTPS_PROXY=https://attacker.invalid",
		"HTTP_PROXY=http://127.0.0.1:7890", "ALL_PROXY=socks5://[::1]:7891",
		"OTEL_EXPORTER_OTLP_HEADERS=authorization=secret", "GROK_INTERNAL_OTLP_HEADERS=secret", // gitleaks:allow -- synthetic rejection fixture
	}, "/isolated/grok", "/official/auth.json")
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"secret", "attacker.invalid", "HOME=/real/home", "GROK_AGENT=", "OTEL_EXPORTER_OTLP_HEADERS="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("environment retained %q:\n%s", forbidden, joined)
		}
	}
	for _, required := range []string{
		"HOME=/isolated/grok", "GROK_HOME=/isolated/grok", "GROK_AUTH_PATH=/official/auth.json",
		"GROK_DISABLE_API_KEY_AUTH=1", "GROK_MAX_RETRIES=3", "GROK_MANAGED_CONFIG=0", "GROK_MANAGED_MCPS_ENABLED=0",
		"GROK_TELEMETRY_ENABLED=0", "OTEL_SDK_DISABLED=true", "GROK_CURSOR_HOOKS_ENABLED=0",
		"GROK_VOICE_MODE=0", "GROK_SCHEDULER_BACKGROUND_LOOPS=0",
		"HTTP_PROXY=http://127.0.0.1:7890", "ALL_PROXY=socks5://[::1]:7891",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("environment missing %q:\n%s", required, joined)
		}
	}
}

func TestGrokWindowsAuthPathAndIsolatedEnvironment(t *testing.T) {
	profile := filepath.Join("C:\\", "Users", "tester")
	wantAuth := filepath.Join(profile, ".grok", "auth.json")
	if got := grokAuthPathFromEnvironmentForOS([]string{
		"UserProfile=" + profile,
		"Home=C:\\unsafe-home",
	}, "windows"); got != wantAuth {
		t.Fatalf("Windows auth path=%q, want %q", got, wantAuth)
	}
	explicit := filepath.Join(profile, ".grok", "alternate.json")
	if got := grokAuthPathFromEnvironmentForOS([]string{
		"grok_auth_path=" + explicit,
		"USERPROFILE=" + profile,
	}, "windows"); got != explicit {
		t.Fatalf("explicit Windows auth path=%q, want %q", got, explicit)
	}

	isolated := filepath.Join("C:\\", "Temp", "carina-grok")
	env := grokCLIEnvironmentForOS([]string{
		`Path=C:\Windows\System32`, `SystemRoot=C:\Windows`, `PATHEXT=.COM;.EXE`,
		"UserProfile=" + profile, `APPDATA=C:\Users\tester\AppData\Roaming`,
	}, isolated, explicit, "windows")
	joined := strings.Join(env, "\n")
	for _, required := range []string{
		`Path=C:\Windows\System32`, `SystemRoot=C:\Windows`, `PATHEXT=.COM;.EXE`,
		"HOME=" + isolated, "USERPROFILE=" + isolated, "GROK_HOME=" + isolated,
		"GROK_AUTH_PATH=" + explicit,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("Windows Grok environment missing %q:\n%s", required, joined)
		}
	}
	for _, forbidden := range []string{"UserProfile=" + profile, "APPDATA="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Windows Grok environment retained %q:\n%s", forbidden, joined)
		}
	}
}

func TestSafeGrokLoopbackProxy(t *testing.T) {
	for _, value := range []string{
		"http://127.0.0.1:7890", "https://localhost:7890", "socks5://[::1]:7890", "socks5h://127.0.0.1:7890",
	} {
		if !provider.IsSafeGrokBuildLoopbackProxy(value) {
			t.Fatalf("loopback proxy rejected: %s", value)
		}
	}
	for _, value := range []string{
		"https://proxy.example:7890", "http://user:pass@127.0.0.1:7890", "file:///tmp/proxy", "http://127.0.0.1:7890/path", "http://127.0.0.1:7890?token=secret",
	} {
		if provider.IsSafeGrokBuildLoopbackProxy(value) {
			t.Fatalf("unsafe proxy accepted: %s", value)
		}
	}
}

func TestPrepareGrokIsolationPinsEmptyBundleAndStrictSandbox(t *testing.T) {
	home := t.TempDir()
	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "auth.json")
	writeOwnerOnlyFile(t, authPath, `{}`)
	configPath, err := prepareGrokIsolation(home, authPath)
	if err != nil {
		t.Fatal(err)
	}
	config := readTestFile(t, configPath)
	for _, required := range []string{"remote_fetch = false", "managed_config = false", "telemetry = false", "voice_mode = false", "enabled = false"} {
		if !strings.Contains(config, required) {
			t.Fatalf("config missing %q:\n%s", required, config)
		}
	}
	sandbox := readTestFile(t, filepath.Join(home, "sandbox.toml"))
	if !strings.Contains(sandbox, `extends = "strict"`) || !strings.Contains(sandbox, `restrict_network = true`) || !strings.Contains(sandbox, authDir) {
		t.Fatalf("sandbox is not strict and auth-scoped: %s", sandbox)
	}
	manifest := readTestFile(t, filepath.Join(home, "bundled", "manifest.json"))
	if manifest != "{\"version\":\"carina-isolated\",\"checksums\":{}}\n" {
		t.Fatalf("unexpected bundle manifest %q", manifest)
	}
}

func TestPrepareGrokIsolationCopiesOnlySanitizedOfficialModelCache(t *testing.T) {
	home := t.TempDir()
	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "auth.json")
	writeOwnerOnlyFile(t, authPath, `{}`)
	sourceFetchedAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	writeOwnerOnlyFile(t, filepath.Join(authDir, "models_cache.json"), `{
		"fetched_at":`+strconv.Quote(sourceFetchedAt)+`,
		"grok_version":"1.0.3",
		"auth_method":"session",
		"origin":"https://cli-chat-proxy.grok.com/v1/models",
		"etag":"public-etag",
		"models":{
			"grok-4.6":{
				"api_base_url":null,
				"api_key":null,
				"env_key":null,
				"info":{"base_url":"https://cli-chat-proxy.grok.com/v1","auth_scheme":"bearer","api_backend":"responses","id":"grok-4.6","model":"grok-4.6","extra_headers":{},"name":"Grok 4.6"}
			},
			"grok-4.5":{
				"api_base_url":null,
				"api_key":null,
				"env_key":null,
				"info":{"base_url":"https://cli-chat-proxy.grok.com/v1","auth_scheme":"bearer","api_backend":"responses","id":"grok-4.5","model":"grok-4.5","extra_headers":{},"name":"Grok 4.5"}
			}
		}
	}`)

	if _, err := prepareGrokIsolation(home, authPath); err != nil {
		t.Fatal(err)
	}
	cache := readTestFile(t, filepath.Join(home, "models_cache.json"))
	for _, forbidden := range []string{"must-not-survive", "MUST_NOT_SURVIVE", "refresh_token"} {
		if strings.Contains(cache, forbidden) {
			t.Fatalf("sanitized cache retained %q: %s", forbidden, cache)
		}
	}
	if !strings.Contains(cache, `"grok-4.6"`) || !strings.Contains(cache, `"name":"Grok 4.6"`) {
		t.Fatalf("sanitized cache lost public model metadata: %s", cache)
	}
	if strings.Index(cache, `"grok-4.6"`) > strings.Index(cache, `"grok-4.5"`) {
		t.Fatal("isolated cache did not preserve the official model ordering")
	}
	if strings.Contains(cache, sourceFetchedAt) {
		t.Fatal("isolated cache TTL was not renewed after validating the official snapshot")
	}
}

func TestCopySanitizedGrokModelsCacheRejectsCredentialBearingModel(t *testing.T) {
	home := t.TempDir()
	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "auth.json")
	writeOwnerOnlyFile(t, authPath, `{}`)
	writeOwnerOnlyFile(t, filepath.Join(authDir, "models_cache.json"), `{
		"fetched_at":`+strconv.Quote(time.Now().UTC().Format(time.RFC3339Nano))+`,
		"grok_version":"1.0.3",
		"auth_method":"session",
		"origin":"https://cli-chat-proxy.grok.com/v1/models",
		"etag":"public-etag",
		"models":{"grok-4.6":{
			"api_base_url":null,
			"api_key":null,
			"env_key":null,
			"info":{"base_url":"https://cli-chat-proxy.grok.com/v1","auth_scheme":"bearer","api_backend":"responses","id":"grok-4.6","model":"grok-4.6","extra_headers":{},"refresh_token":"must-not-survive"}
		}}
	}`)
	if err := copySanitizedGrokModelsCache(home, authPath); err == nil {
		t.Fatal("credential-bearing model cache must be rejected")
	}
}

func TestCopySanitizedGrokModelsCacheRejectsUnknownMetadataAndVersionMismatch(t *testing.T) {
	for _, test := range []struct {
		name            string
		version         string
		extraInfo       string
		expectedVersion string
	}{
		{name: "unknown metadata", version: "1.0.3", extraInfo: `,"futureCapability":false`, expectedVersion: "1.0.3"},
		{name: "version mismatch", version: "1.0.2", expectedVersion: "1.0.3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			authDir := t.TempDir()
			authPath := filepath.Join(authDir, "auth.json")
			writeOwnerOnlyFile(t, authPath, `{}`)
			writeOwnerOnlyFile(t, filepath.Join(authDir, "models_cache.json"), `{
				"fetched_at":`+strconv.Quote(time.Now().UTC().Format(time.RFC3339Nano))+`,
				"grok_version":`+strconv.Quote(test.version)+`,
				"auth_method":"session",
				"origin":"https://cli-chat-proxy.grok.com/v1/models",
				"models":{"grok-4.6":{
					"api_base_url":null,"api_key":null,"env_key":null,
					"info":{"base_url":"https://cli-chat-proxy.grok.com/v1","auth_scheme":"bearer","api_backend":"responses","id":"grok-4.6","model":"grok-4.6","extra_headers":{}`+test.extraInfo+`}
				}}
			}`)
			if err := copySanitizedGrokModelsCacheForVersion(home, authPath, test.expectedVersion); err == nil {
				t.Fatal("unsafe model cache was accepted")
			}
		})
	}
}

func TestCopySanitizedGrokModelsCacheRejectsNonOfficialRoutes(t *testing.T) {
	for _, origin := range []string{
		"https://attacker.invalid/v1/models",
		"https://cli-chat-proxy.grok.com:444/v1/models",
		"https://cli-chat-proxy.grok.com/v1/models?token=secret",
	} {
		t.Run(origin, func(t *testing.T) {
			home := t.TempDir()
			authDir := t.TempDir()
			authPath := filepath.Join(authDir, "auth.json")
			writeOwnerOnlyFile(t, authPath, `{}`)
			writeOwnerOnlyFile(t, filepath.Join(authDir, "models_cache.json"), `{
				"fetched_at":`+strconv.Quote(time.Now().UTC().Format(time.RFC3339Nano))+`,
				"grok_version":"1.0.3",
				"auth_method":"session",
				"origin":`+strconv.Quote(origin)+`,
				"etag":"public-etag",
				"models":{"grok-4.6":{"api_base_url":null}}
			}`)
			if err := copySanitizedGrokModelsCache(home, authPath); err == nil {
				t.Fatal("non-official model route must be rejected")
			}
		})
	}
}

func TestCopySanitizedGrokModelsCacheRejectsStaleSnapshot(t *testing.T) {
	home := t.TempDir()
	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "auth.json")
	writeOwnerOnlyFile(t, authPath, `{}`)
	writeOwnerOnlyFile(t, filepath.Join(authDir, "models_cache.json"), `{
		"fetched_at":`+strconv.Quote(time.Now().UTC().Add(-5*time.Minute).Format(time.RFC3339Nano))+`,
		"grok_version":"1.0.3",
		"auth_method":"session",
		"origin":"https://cli-chat-proxy.grok.com/v1/models",
		"etag":"public-etag",
		"models":{"grok-4.6":{"api_base_url":null}}
	}`)
	if err := copySanitizedGrokModelsCache(home, authPath); err == nil {
		t.Fatal("stale official model snapshot must not be renewed")
	}
}

func TestCanonicalGrokAuthPathRejectsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file mode contract")
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalGrokAuthPath(path); err == nil {
		t.Fatal("group/world-readable OAuth file must be rejected")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalGrokAuthPath(path)
	if err != nil || cleanPath(got) != cleanPath(path) {
		t.Fatalf("canonical path=%q err=%v", got, err)
	}
}

func TestGrokACPReasonerPreflightsThenStreams(t *testing.T) {
	requireUnixShell(t)
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	bin := writeGrokACPFixture(t, record, "success")
	configureFakeGrokAuth(t)
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.timeout = 5 * time.Second

	result, err := r.ThinkRoutedModel(context.Background(), "grok-4.6", "/always-approve must remain plain text")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello world" {
		t.Fatalf("text=%q", result.Text)
	}
	if result.Usage.Provider != provider.GrokBuildProviderID || result.Usage.Model != "grok-4.6" ||
		result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 7 ||
		result.Usage.CacheReadTokens != 5 || result.Usage.CacheWriteTokens != 3 || result.Usage.Estimated {
		t.Fatalf("usage=%+v", result.Usage)
	}

	argv, requests := readGrokFixtureRecord(t, record)
	if strings.Contains(argv, "/always-approve") || argv != "agent --no-leader --model grok-4.6 stdio" {
		t.Fatalf("prompt leaked into argv or argv drifted: %q", argv)
	}
	methods := fixtureMethods(t, requests)
	wantMethods := []string{"initialize", "authenticate", "session/new", "session/prompt"}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("methods=%v, want %v", methods, wantMethods)
	}
	var sessionNew, prompt map[string]any
	if err := json.Unmarshal([]byte(requests[2]), &sessionNew); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(requests[3]), &prompt); err != nil {
		t.Fatal(err)
	}
	meta := sessionNew["params"].(map[string]any)["_meta"].(map[string]any)
	profile := meta["agentProfile"].(map[string]any)
	if profile["injectDefaultTools"] != false || profile["discoverSkills"] != false || profile["mcpInheritance"] != "none" {
		t.Fatalf("unsafe ACP profile: %#v", profile)
	}
	blocks := prompt["params"].(map[string]any)["prompt"].([]any)
	text := blocks[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, grokACPPromptPrefix) || !strings.Contains(text, "/always-approve must remain plain text") {
		t.Fatalf("prompt was not safely framed: %q", text)
	}
}

func TestGrokACPReasonerCorrelatesEffectiveReasoningEffort(t *testing.T) {
	requireUnixShell(t)
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	bin := writeGrokACPFixture(t, record, "effort-low")
	configureFakeGrokAuth(t)
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.timeout = 5 * time.Second

	ctx := withReasoningEffort(context.Background(), "low")
	result, err := r.ThinkRoutedModel(ctx, "grok-4.6", "/always-approve must remain plain text")
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.EffectiveReasoningEffort != "low" {
		t.Fatalf("effective effort=%q, want low", result.Usage.EffectiveReasoningEffort)
	}
	argv, _ := readGrokFixtureRecord(t, record)
	if argv != "agent --no-leader --model grok-4.6 --reasoning-effort low stdio" {
		t.Fatalf("argv=%q", argv)
	}
}

func TestGrokACPResponsesRailCompletesWithoutMessagesStart(t *testing.T) {
	requireUnixShell(t)
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	bin := writeGrokACPFixture(t, record, "responses-rail")
	configureFakeGrokAuth(t)
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.timeout = 5 * time.Second

	result, err := r.ThinkRoutedModel(context.Background(), "grok-4.6", "/always-approve must remain plain text")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello world" {
		t.Fatalf("text=%q", result.Text)
	}
}

func TestGrokACPRefusesToolsBeforeSendingPrompt(t *testing.T) {
	requireUnixShell(t)
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	bin := writeGrokACPFixture(t, record, "unsafe-tools")
	configureFakeGrokAuth(t)
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.timeout = 5 * time.Second

	_, err = r.ThinkRoutedModel(context.Background(), "grok-4.6", "must never be sent")
	info := classifyProviderError(err)
	if err == nil || info.Code != "reasoner_safety_violation" {
		t.Fatalf("err=%v info=%+v", err, info)
	}
	_, requests := readGrokFixtureRecord(t, record)
	if methods := fixtureMethods(t, requests); !reflect.DeepEqual(methods, []string{"initialize", "authenticate", "session/new"}) {
		t.Fatalf("prompt crossed failed capability gate: %v", methods)
	}
}

func TestGrokACPWireEnvelopeRequiresExactMessageKinds(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		kind grokACPWireKind
	}{
		{name: "notification", raw: `{"jsonrpc":"2.0","method":"session/update","params":{}}`, kind: grokACPWireNotification},
		{name: "client request", raw: `{"jsonrpc":"2.0","id":9,"method":"fs/read_text_file","params":{}}`, kind: grokACPWireRequest},
		{name: "success", raw: `{"jsonrpc":"2.0","id":1,"result":{}}`, kind: grokACPWireSuccess},
		{name: "failure", raw: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"request failed","data":null}}`, kind: grokACPWireFailure},
	} {
		t.Run("accept "+test.name, func(t *testing.T) {
			message, err := decodeGrokACPWireMessage([]byte(test.raw))
			if err != nil || message.Kind != test.kind {
				t.Fatalf("kind=%v err=%v", message.Kind, err)
			}
		})
	}

	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "unknown outer field", raw: `{"jsonrpc":"2.0","id":1,"result":{},"future":true}`},
		{name: "result and error", raw: `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32000,"message":"failed"}}`},
		{name: "response params tool payload", raw: `{"jsonrpc":"2.0","id":1,"result":{},"params":{"tool":"read_file"}}`},
		{name: "notification result", raw: `{"jsonrpc":"2.0","method":"session/update","params":{},"result":{}}`},
		{name: "notification unknown outer field", raw: `{"jsonrpc":"2.0","method":"session/update","params":{},"future":true}`},
		{name: "error unknown field", raw: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"failed","tool":"read_file"}}`},
		{name: "duplicate outer field", raw: `{"jsonrpc":"2.0","id":1,"result":{},"result":{"tool":"read_file"}}`},
		{name: "duplicate error field", raw: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"failed","message":"tool"}}`},
		{name: "duplicate nested auth method", raw: `{"jsonrpc":"2.0","id":1,"result":{"authMethods":[{"id":"cached_token","id":"xai.api_key"}]}}`},
		{name: "duplicate nested tool list", raw: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"available_commands_update","_meta":{"tools":[],"tools":["read_file"]}}}}`},
		{name: "duplicate nested terminal usage", raw: `{"jsonrpc":"2.0","id":5,"result":{"_meta":{"usage":{"totalTokens":1,"totalTokens":2}}}}`},
		{name: "response without terminal", raw: `{"jsonrpc":"2.0","id":1}`},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			if _, err := decodeGrokACPWireMessage([]byte(test.raw)); err == nil {
				t.Fatal("inexact ACP envelope was accepted")
			}
		})
	}

	client := newGrokACPClient(strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"fs/read_text_file","params":{}}`+"\n"), io.Discard, "grok-4.6", t.TempDir(), nil)
	_, err := client.response(context.Background(), 1, grokACPPreflight)
	if info := classifyProviderError(err); err == nil || info.Code != "reasoner_safety_violation" {
		t.Fatalf("client capability request err=%v info=%+v", err, info)
	}
}

func TestGrokACPHandshakeResultsRejectUnknownCapabilities(t *testing.T) {
	workdir := t.TempDir()
	newInitialize := func() map[string]any {
		var capabilities any
		if err := json.Unmarshal([]byte(grokACPFixtureAgentCapabilities), &capabilities); err != nil {
			t.Fatal(err)
		}
		return map[string]any{
			"protocolVersion":   "1",
			"agentCapabilities": capabilities,
			"authMethods":       []any{map[string]any{"id": "cached_token", "name": "cached_token"}},
			"_meta": map[string]any{
				"grokShell": true, "defaultAuthMethodId": "cached_token",
				"availableCommands": []any{
					map[string]any{"name": "compact"}, map[string]any{"name": "always-approve"},
					map[string]any{"name": "context"}, map[string]any{"name": "session-info"},
				},
			},
		}
	}
	validateInitialize := func(response map[string]any) error {
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return validateGrokACPInitialize(raw, "grok-4.6", workdir)
	}
	if err := validateInitialize(newInitialize()); err != nil {
		t.Fatalf("legal initialize result rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown result field", mutate: func(response map[string]any) { response["future"] = true }},
		{name: "unknown agent capability", mutate: func(response map[string]any) {
			response["agentCapabilities"].(map[string]any)["toolExecution"] = true
		}},
		{name: "unknown prompt capability", mutate: func(response map[string]any) {
			response["agentCapabilities"].(map[string]any)["promptCapabilities"].(map[string]any)["tools"] = true
		}},
		{name: "unknown initialize metadata", mutate: func(response map[string]any) {
			response["_meta"].(map[string]any)["futureCapability"] = true
		}},
		{name: "unknown auth method field", mutate: func(response map[string]any) {
			response["authMethods"].([]any)[0].(map[string]any)["token"] = "opaque"
		}},
		{name: "unknown command field", mutate: func(response map[string]any) {
			response["_meta"].(map[string]any)["availableCommands"].([]any)[0].(map[string]any)["tool"] = "read_file"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := newInitialize()
			test.mutate(response)
			if err := validateInitialize(response); err == nil {
				t.Fatal("inexact initialize result was accepted")
			}
		})
	}

	legalAuth := json.RawMessage(`{"_meta":{"email":null,"auth_mode":"GrokCom","team_id":null,"team_name":null,"is_zdr":false,"team_role":null,"coding_data_retention_opt_out":true,"show_resolved_model":null,"gate":null,"subscription_tier":"SuperGrok"}}`)
	if err := validateGrokACPAuthenticate(legalAuth); err != nil {
		t.Fatalf("legal authenticate metadata rejected: %v", err)
	}
	if err := validateGrokACPAuthenticate(json.RawMessage(`{"_meta":{"auth_mode":"Oidc"}}`)); err != nil {
		t.Fatalf("OIDC authenticate metadata rejected: %v", err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"future":true}`),
		json.RawMessage(`{"_meta":{"futureCapability":true}}`),
		json.RawMessage(`{"_meta":{},"_meta":{"futureCapability":true}}`),
		json.RawMessage(`{"_meta":{"auth_mode":"ApiKey"}}`),
		json.RawMessage(`{"_meta":{"auth_mode":"WebLogin"}}`),
		json.RawMessage(`{"_meta":{"auth_mode":"External"}}`),
	} {
		if err := validateGrokACPAuthenticate(raw); err == nil {
			t.Fatalf("inexact authenticate result was accepted: %s", raw)
		}
	}
}

func TestGrokACPNewSessionResultRequiresExactIsolatedShape(t *testing.T) {
	workdir := t.TempDir()
	newResponse := func() map[string]any {
		return map[string]any{
			"sessionId": "session-1",
			"models":    map[string]any{"currentModelId": "grok-4.6", "availableModels": []any{}},
			"_meta": map[string]any{
				"currentWorkingDirectory": workdir,
				"feedbackEnabled":         false,
				"codebaseIndexed":         []any{},
				"isGitRepo":               false,
				"gitRoot":                 nil,
				"showNonGitWarning":       true,
			},
		}
	}
	accept := func(response map[string]any) error {
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", workdir, nil)
		client.pendingSessionID = "session-1"
		return client.acceptNewSession(raw)
	}
	if err := accept(newResponse()); err != nil {
		t.Fatalf("legal session result rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown result field", mutate: func(response map[string]any) { response["future"] = true }},
		{name: "unknown model field", mutate: func(response map[string]any) {
			response["models"].(map[string]any)["toolCapabilities"] = []any{"read_file"}
		}},
		{name: "unknown session metadata", mutate: func(response map[string]any) {
			response["_meta"].(map[string]any)["futureCapability"] = true
		}},
		{name: "tool override", mutate: func(response map[string]any) {
			response["_meta"].(map[string]any)["toolOverrides"] = map[string]any{"tools": []any{"read_file"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := newResponse()
			test.mutate(response)
			if err := accept(response); err == nil {
				t.Fatal("inexact session result was accepted")
			}
		})
	}
}

func TestGrokACPPreflightPushesRequireExactSafePayloads(t *testing.T) {
	client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
	for _, test := range []struct {
		method string
		raw    json.RawMessage
	}{
		{method: "x.ai/settings/update", raw: json.RawMessage(`{}`)},
		{method: "_x.ai/settings/update", raw: json.RawMessage(`{"method":"x.ai/settings/update","params":{"sharing_enabled":false,"auto_permission_mode_enabled":false,"permission_mode":"always-approve"}}`)},
		{method: "x.ai/announcements/update", raw: json.RawMessage(`{"gen":1,"announcements":[]}`)},
		{method: "_x.ai/announcements/update", raw: json.RawMessage(`{"method":"x.ai/announcements/update","params":{"gen":2,"announcements":[]}}`)},
	} {
		if err := client.notification(test.method, test.raw, grokACPPreflight); err != nil {
			t.Fatalf("legal %s rejected: %v", test.method, err)
		}
	}
	for _, test := range []struct {
		name   string
		method string
		raw    json.RawMessage
	}{
		{name: "settings extra field", method: "x.ai/settings/update", raw: json.RawMessage(`{"futureCapability":true}`)},
		{name: "settings auto mode", method: "x.ai/settings/update", raw: json.RawMessage(`{"auto_permission_mode_enabled":true}`)},
		{name: "wrapped settings extra field", method: "_x.ai/settings/update", raw: json.RawMessage(`{"method":"x.ai/settings/update","params":{},"tool":"read_file"}`)},
		{name: "announcements extra field", method: "x.ai/announcements/update", raw: json.RawMessage(`{"gen":1,"announcements":[],"tool":"read_file"}`)},
		{name: "announcement item extra field", method: "x.ai/announcements/update", raw: json.RawMessage(`{"gen":1,"announcements":[{"message":"hello","tool":"read_file"}]}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := client.notification(test.method, test.raw, grokACPPreflight); err == nil {
				t.Fatal("unsafe preflight push was accepted")
			}
		})
	}
}

func TestGrokACPModelChangedNotificationIsExactAndPreflightOnly(t *testing.T) {
	valid := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"model_changed","model_id":"grok-4.6","reasoning_effort":"high"}}`)
	for _, test := range []struct {
		name            string
		phase           grokACPPhase
		raw             json.RawMessage
		requestedEffort string
		accept          bool
	}{
		{name: "exact preflight", phase: grokACPPreflight, raw: valid, accept: true},
		{name: "matching requested effort", phase: grokACPPreflight, raw: valid, requestedEffort: "high", accept: true},
		{name: "mismatched requested effort", phase: grokACPPreflight, raw: json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"model_changed","model_id":"grok-4.6","reasoning_effort":"low"}}`), requestedEffort: "high"},
		{name: "missing requested effort", phase: grokACPPreflight, raw: json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"model_changed","model_id":"grok-4.6"}}`), requestedEffort: "high"},
		{name: "prompt phase", phase: grokACPPrompt, raw: valid},
		{name: "wrong model", phase: grokACPPreflight, raw: json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"model_changed","model_id":"grok-4.5"}}`)},
		{name: "extra field", phase: grokACPPreflight, raw: json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"model_changed","model_id":"grok-4.6","unsafe":true}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
			client.sessionRequested = true
			client.requestedEffort = test.requestedEffort
			err := client.notification("_x.ai/session_notification", test.raw, test.phase)
			if (err == nil) != test.accept {
				t.Fatalf("err=%v accept=%v", err, test.accept)
			}
			if test.accept && strings.Contains(string(test.raw), `"reasoning_effort"`) && client.effectiveEffort == "" {
				t.Fatal("accepted effort was not recorded as effective")
			}
		})
	}
}

func TestGrokACPInferenceLifecycleAllowsOnlyInertEvents(t *testing.T) {
	client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
	client.sessionID = "session-1"
	client.commandsVerified = true
	client.userEchoVerified = true
	valid := []json.RawMessage{
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"response_started","message_id":"msg-1","model":"grok-4.6","input_tokens":12,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"reasoning_completed","signature":"opaque"}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"session_summary_generated","session_summary":"Fixed short title"}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"retry_state","type":"retrying","attempt":1,"max_retries":15,"reason":"transient"},"_meta":{"eventId":"session-1-retry","agentTimestampMs":1}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"response_completed","message_id":"msg-1","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"reasoning_tokens":0}}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"turn_completed","prompt_id":"carina-one-shot","stop_reason":"end_turn","agent_result":"OK","usage":` + grokACPFixturePromptUsage + `},"_meta":{"eventId":"session-1-1","agentTimestampMs":1}}`),
	}
	for index, event := range valid {
		if err := client.notification("_x.ai/session_notification", event, grokACPPrompt); err != nil {
			t.Fatalf("valid inference lifecycle rejected: %v", err)
		}
		if index == 0 {
			client.text.WriteString("OK")
		}
	}
	promptComplete := json.RawMessage(`{"sessionId":"session-1","promptId":"carina-one-shot","stopReason":"end_turn","agentResult":"OK"}`)
	if err := client.notification("_x.ai/session/prompt_complete", promptComplete, grokACPPrompt); err != nil {
		t.Fatalf("valid prompt completion rejected: %v", err)
	}
	for _, terminal := range []struct {
		raw      json.RawMessage
		contains string
	}{
		{raw: json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"retry_state","type":"exhausted","attempts":15,"reason":"transient","is_rate_limited":true},"_meta":{"eventId":"session-1-retry-exhausted","agentTimestampMs":1}}`), contains: "rate limit"},
		{raw: json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"retry_state","type":"failed","error_type":"auth","message":"request rejected"},"_meta":{"eventId":"session-1-retry-failed","agentTimestampMs":1}}`), contains: "grok login"},
	} {
		fresh := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
		fresh.sessionID = "session-1"
		fresh.commandsVerified = true
		fresh.userEchoVerified = true
		err := fresh.notification("_x.ai/session_notification", terminal.raw, grokACPPrompt)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), terminal.contains) {
			t.Fatalf("terminal retry state was not surfaced safely: %v", err)
		}
	}
	titleUpdate := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"session_info_update","title":"Fixed short title"}}`)
	if err := client.sessionUpdate(titleUpdate, grokACPPrompt); err != nil {
		t.Fatalf("matching session title update rejected: %v", err)
	}
	changedTitle := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"session_info_update","title":"Changed title"}}`)
	if err := client.sessionUpdate(changedTitle, grokACPPrompt); err == nil {
		t.Fatal("uncorrelated session title update was accepted")
	}
	for _, event := range []json.RawMessage{
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"tool_call_delta_chunk","tool_call_id":"tool-1","name":"shell","arguments_delta":"{}"},"_meta":{"eventId":"session-1-tool","agentTimestampMs":1}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"response_completed","stop_reason":"tool_use"}}`),
		json.RawMessage(`{"sessionId":"session-2","update":{"sessionUpdate":"response_started","model":"grok-4.6","input_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"retry_state","type":"retrying","attempt":4,"max_retries":3,"reason":"transient"},"_meta":{"eventId":"session-1-retry","agentTimestampMs":1}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"retry_state","type":"retrying","attempt":1,"max_retries":16,"reason":"transient"},"_meta":{"eventId":"session-1-retry","agentTimestampMs":1}}`),
	} {
		if err := client.notification("_x.ai/session_notification", event, grokACPPrompt); err == nil {
			t.Fatalf("unsafe inference event was accepted: %s", grokACPNotificationDescriptor("_x.ai/session_notification", event))
		}
	}
}

func TestGrokACPTextChunksRequireExactCurrentResponseShape(t *testing.T) {
	newClient := func() *grokACPClient {
		client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
		client.sessionID = "session-1"
		client.commandsVerified = true
		client.userEchoVerified = true
		client.responseStarted = true
		return client
	}
	thought := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hidden"}},"_meta":{"eventId":"thought-1","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentThoughtChunk","chunkId":0,"totalTokens":20}}`)
	message := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}},"_meta":{"eventId":"message-1","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentMessageChunk","chunkId":1,"totalTokens":20}}`)
	client := newClient()
	if err := client.sessionUpdate(thought, grokACPPrompt); err != nil {
		t.Fatalf("exact thought chunk rejected: %v", err)
	}
	if err := client.sessionUpdate(message, grokACPPrompt); err != nil {
		t.Fatalf("exact message chunk rejected: %v", err)
	}
	if client.thoughtBytes != len("hidden") || client.text.String() != "hello" {
		t.Fatalf("thought bytes=%d text=%q", client.thoughtBytes, client.text.String())
	}

	for name, raw := range map[string]json.RawMessage{
		"missing envelope meta": json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hidden"}}}`),
		"extra envelope field":  json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hidden"}},"_meta":{"eventId":"thought-1","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentThoughtChunk","chunkId":0,"totalTokens":20},"unsafe":true}`),
		"extra content field":   json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hidden","unsafe":true}},"_meta":{"eventId":"thought-1","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentThoughtChunk","chunkId":0,"totalTokens":20}}`),
		"non-text thought":      json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"image","text":"hidden"}},"_meta":{"eventId":"thought-1","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentThoughtChunk","chunkId":0,"totalTokens":20}}`),
		"wrong update type":     json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hidden"}},"_meta":{"eventId":"thought-1","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentMessageChunk","chunkId":0,"totalTokens":20}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := newClient().sessionUpdate(raw, grokACPPrompt); err == nil {
				t.Fatal("malformed thought chunk was accepted")
			}
		})
	}
	completed := newClient()
	completed.responseCompleted = true
	if err := completed.sessionUpdate(message, grokACPPrompt); err == nil {
		t.Fatal("message chunk after response completion was accepted")
	}
}

func TestGrokACPTerminalLifecycleIsSingleOrderedAndCorrelated(t *testing.T) {
	type lifecycleEvent struct {
		method string
		raw    json.RawMessage
	}
	start := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"response_started","message_id":"msg-1","model":"grok-4.6","input_tokens":12,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
	responseComplete := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"response_completed","message_id":"msg-1","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"reasoning_tokens":0}}}`)
	turnComplete := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"turn_completed","prompt_id":"carina-one-shot","stop_reason":"end_turn","agent_result":"hello","usage":` + grokACPFixturePromptUsage + `},"_meta":{"eventId":"turn-complete-1","agentTimestampMs":1}}`)
	promptComplete := json.RawMessage(`{"sessionId":"session-1","promptId":"carina-one-shot","stopReason":"end_turn","agentResult":"hello"}`)
	newClient := func() *grokACPClient {
		client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
		client.sessionID = "session-1"
		client.commandsVerified = true
		client.userEchoVerified = true
		return client
	}
	notify := func(client *grokACPClient, method string, raw json.RawMessage) error {
		return client.notification(method, raw, grokACPPrompt)
	}
	apply := func(t *testing.T, client *grokACPClient, events ...lifecycleEvent) {
		t.Helper()
		for _, event := range events {
			if err := notify(client, event.method, event.raw); err != nil {
				t.Fatalf("valid lifecycle prefix rejected: %v", err)
			}
			if client.responseStarted && client.text.Len() == 0 {
				client.text.WriteString("hello")
			}
		}
	}
	xai := func(raw json.RawMessage) lifecycleEvent {
		return lifecycleEvent{method: "_x.ai/session_notification", raw: raw}
	}
	prompt := func(raw json.RawMessage) lifecycleEvent {
		return lifecycleEvent{method: "_x.ai/session/prompt_complete", raw: raw}
	}

	for _, test := range []struct {
		name   string
		prefix []lifecycleEvent
		bad    lifecycleEvent
	}{
		{name: "response completed before start", bad: xai(responseComplete)},
		{name: "duplicate response start", prefix: []lifecycleEvent{xai(start)}, bad: xai(start)},
		{name: "turn completed before response", prefix: []lifecycleEvent{xai(start)}, bad: xai(turnComplete)},
		{name: "duplicate response completed", prefix: []lifecycleEvent{xai(start), xai(responseComplete)}, bad: xai(responseComplete)},
		{name: "prompt completed before turn", prefix: []lifecycleEvent{xai(start), xai(responseComplete)}, bad: prompt(promptComplete)},
		{name: "duplicate turn completed", prefix: []lifecycleEvent{xai(start), xai(responseComplete), xai(turnComplete)}, bad: xai(turnComplete)},
		{name: "duplicate prompt completed", prefix: []lifecycleEvent{xai(start), xai(responseComplete), xai(turnComplete), prompt(promptComplete)}, bad: prompt(promptComplete)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newClient()
			apply(t, client, test.prefix...)
			if err := notify(client, test.bad.method, test.bad.raw); err == nil {
				t.Fatal("duplicate or out-of-order terminal event was accepted")
			}
		})
	}

	wrongMessageID := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"response_completed","message_id":"msg-2","stop_reason":"end_turn"}}`)
	client := newClient()
	apply(t, client, xai(start))
	if err := notify(client, "_x.ai/session_notification", wrongMessageID); err == nil {
		t.Fatal("mismatched response message id was accepted")
	}
	wrongResult := json.RawMessage(`{"sessionId":"session-1","promptId":"carina-one-shot","stopReason":"end_turn","agentResult":"changed"}`)
	client = newClient()
	apply(t, client, xai(start), xai(responseComplete), xai(turnComplete))
	if err := notify(client, "_x.ai/session/prompt_complete", wrongResult); err == nil {
		t.Fatal("prompt completion result that differed from streamed text was accepted")
	}
}

func TestGrokACPUserEchoMustMatchCurrentPlainPrompt(t *testing.T) {
	client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
	client.sessionID = "session-1"
	client.commandsVerified = true
	client.promptText = grokACPPromptPrefix + "prompt"
	valid := json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":` + strconv.Quote(client.promptText) + `},"_meta":{"modelId":"grok-4.6","promptIndex":0}},"_meta":{"eventId":"session-1-echo","agentTimestampMs":1}}`)
	if err := client.sessionUpdate(valid, grokACPPrompt); err != nil {
		t.Fatalf("valid prompt echo rejected: %v", err)
	}
	if !client.userEchoVerified {
		t.Fatal("valid prompt echo was not recorded")
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"changed"},"_meta":{"modelId":"grok-4.6","promptIndex":0}},"_meta":{"eventId":"session-1-echo","agentTimestampMs":1}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"image","data":"unsafe"},"_meta":{"modelId":"grok-4.6","promptIndex":0}},"_meta":{"eventId":"session-1-echo","agentTimestampMs":1}}`),
		json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":` + strconv.Quote(client.promptText) + `},"_meta":{"modelId":"grok-4.5","promptIndex":0}},"_meta":{"eventId":"session-1-echo","agentTimestampMs":1}}`),
	} {
		fresh := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
		fresh.sessionID = "session-1"
		fresh.commandsVerified = true
		fresh.promptText = client.promptText
		if err := fresh.sessionUpdate(invalid, grokACPPrompt); err == nil {
			t.Fatal("invalid prompt echo was accepted")
		}
	}
}

func TestGrokACPSessionRosterUpdateIsCurrentSessionOnly(t *testing.T) {
	workdir := t.TempDir()
	client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", workdir, nil)
	client.sessionID = "session-1"
	valid := json.RawMessage(`{"upserted":[{"sessionId":"session-1","title":null,"cwd":` + strconv.Quote(workdir) + `,"isWorktree":false,"modelId":"grok-4.6","yolo":false,"activity":"working","resident":true,"lastChangeUnixMs":1,"origin":{"kind":"local"}}],"removed":[]}`)
	if err := client.notification("_x.ai/sessions/changed", valid, grokACPPrompt); err != nil {
		t.Fatalf("valid roster update rejected: %v", err)
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"upserted":[{"sessionId":"session-2","title":null,"cwd":` + strconv.Quote(workdir) + `,"isWorktree":false,"modelId":"grok-4.6","yolo":false,"activity":"working","resident":true,"lastChangeUnixMs":1,"origin":{"kind":"local"}}],"removed":[]}`),
		json.RawMessage(`{"upserted":[{"sessionId":"session-1","title":null,"cwd":` + strconv.Quote(workdir) + `,"isWorktree":false,"modelId":"grok-4.6","yolo":false,"activity":"needs_input","resident":true,"lastChangeUnixMs":1,"origin":{"kind":"local"}}],"removed":[]}`),
	} {
		if err := client.notification("_x.ai/sessions/changed", invalid, grokACPPrompt); err == nil {
			t.Fatal("unsafe roster update was accepted")
		}
	}
}

func TestGrokACPQueueUpdateOnlyTracksCurrentPlainPrompt(t *testing.T) {
	client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
	client.sessionID = "session-1"
	client.promptText = grokACPPromptPrefix + "prompt"
	valid := []json.RawMessage{
		json.RawMessage(`{"sessionId":"session-1","entries":[{"id":"carina-one-shot","version":0,"kind":"prompt","text":` + strconv.Quote(client.promptText) + `,"position":0}]}`),
		json.RawMessage(`{"sessionId":"session-1","entries":[],"runningPromptId":"carina-one-shot","runningText":` + strconv.Quote(client.promptText) + `,"runningKind":"prompt"}`),
		json.RawMessage(`{"sessionId":"session-1","entries":[]}`),
	}
	for _, event := range valid {
		if err := client.notification("_x.ai/queue/changed", event, grokACPPrompt); err != nil {
			t.Fatalf("valid queue update rejected: %v", err)
		}
	}
	invalid := json.RawMessage(`{"sessionId":"session-1","entries":[],"runningPromptId":"carina-one-shot","runningText":"shell command","runningKind":"bash"}`)
	if err := client.notification("_x.ai/queue/changed", invalid, grokACPPrompt); err == nil {
		t.Fatal("executable queue update was accepted")
	}
}

func TestGrokACPRejectsToolEventAndClassifiesAuthErrors(t *testing.T) {
	requireUnixShell(t)
	for _, test := range []struct {
		mode, code string
	}{
		{"tool-event", "reasoner_safety_violation"},
		{"auth-error", "provider_authentication_failed"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "requests.jsonl")
			bin := writeGrokACPFixture(t, record, test.mode)
			configureFakeGrokAuth(t)
			r, err := newGrokCLIReasoner(bin)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			r.timeout = 5 * time.Second
			_, err = r.ThinkRoutedModel(context.Background(), "grok-4.6", "prompt")
			if info := classifyProviderError(err); err == nil || info.Code != test.code {
				t.Fatalf("err=%v info=%+v", err, info)
			}
		})
	}
}

func TestGrokACPRejectsInvalidTerminalStreamsWithoutWaitingAfterResult(t *testing.T) {
	requireUnixShell(t)
	for _, test := range []struct {
		mode string
		code string
	}{
		{mode: "duplicate-response-start", code: "reasoner_safety_violation"},
		{mode: "mismatched-turn-result", code: "reasoner_safety_violation"},
		{mode: "mismatched-prompt-result", code: "reasoner_protocol_error"},
		{mode: "result-before-terminal", code: "reasoner_protocol_error"},
		{mode: "post-result-event", code: "reasoner_protocol_error"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "requests.jsonl")
			bin := writeGrokACPFixture(t, record, test.mode)
			configureFakeGrokAuth(t)
			r, err := newGrokCLIReasoner(bin)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			r.timeout = 5 * time.Second
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err = r.ThinkRoutedModel(ctx, "grok-4.6", "/always-approve must remain plain text")
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("client waited for a notification after the id=5 result")
			}
			if info := classifyProviderError(err); err == nil || info.Code != test.code {
				t.Fatalf("err=%v info=%+v", err, info)
			}
		})
	}
}

func TestGrokACPRejectsNaturalNonzeroExitAfterValidResult(t *testing.T) {
	requireUnixShell(t)
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	bin := writeGrokACPFixture(t, record, "success")
	file, err := os.OpenFile(bin, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("exit 23\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	configureFakeGrokAuth(t)
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.timeout = 5 * time.Second
	_, err = r.ThinkRoutedModel(context.Background(), "grok-4.6", "/always-approve must remain plain text")
	if err == nil {
		t.Fatal("natural non-zero CLI exit was mistaken for Carina process termination")
	}
}

func TestGrokACPFinishRequiresExactCorrelatedPureResponse(t *testing.T) {
	usage := grokACPUsage{
		grokACPUsageModel: grokACPUsageModel{
			InputTokens:         20,
			OutputTokens:        7,
			TotalTokens:         27,
			CachedReadTokens:    5,
			CacheCreationTokens: 3,
			ReasoningTokens:     2,
			ModelCalls:          1,
			APIDurationMS:       10,
		},
		NumTurns: 1,
	}
	responseUsage := grokACPResponseUsage{
		InputTokens:              12,
		OutputTokens:             7,
		CacheReadInputTokens:     5,
		CacheCreationInputTokens: 3,
		ReasoningTokens:          2,
	}
	newClient := func() *grokACPClient {
		client := newGrokACPClient(strings.NewReader(""), io.Discard, "grok-4.6", t.TempDir(), nil)
		client.sessionID = "session-1"
		client.commandsReplayed = true
		client.responseCompleted = true
		client.turnCompleted = true
		client.promptCompleted = true
		client.responseRail = grokACPResponseRailResponses
		client.responseUsage = &responseUsage
		client.turnUsage = &usage
		client.text.WriteString("hello world")
		return client
	}
	newResponse := func() map[string]any {
		return map[string]any{
			"stopReason": "end_turn",
			"_meta": map[string]any{
				"sessionId":        "session-1",
				"requestId":        "carina-one-shot",
				"promptId":         "carina-one-shot",
				"totalTokens":      27,
				"modelId":          "grok-4.6",
				"inputTokens":      20,
				"outputTokens":     7,
				"cachedReadTokens": 5,
				"reasoningTokens":  2,
				"usage": map[string]any{
					"inputTokens": 20, "outputTokens": 7, "totalTokens": 27,
					"cachedReadTokens": 5, "cacheCreationTokens": 3, "reasoningTokens": 2,
					"modelCalls": 1, "apiDurationMs": 10, "numTurns": 1,
				},
			},
		}
	}

	validRaw, err := json.Marshal(newResponse())
	if err != nil {
		t.Fatal(err)
	}
	result, err := newClient().finish(validRaw)
	if err != nil {
		t.Fatalf("valid correlated response rejected: %v", err)
	}
	if result.Text != "hello world" || result.Usage.InputTokens != 12 || result.Usage.CacheReadTokens != 5 || result.Usage.CacheWriteTokens != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "wrong prompt id",
			mutate: func(response map[string]any) {
				response["_meta"].(map[string]any)["promptId"] = "another-prompt"
			},
		},
		{
			name: "tool overrides",
			mutate: func(response map[string]any) {
				response["_meta"].(map[string]any)["toolOverrides"] = map[string]any{"tools": []string{"read_file"}}
			},
		},
		{
			name: "unknown top-level field",
			mutate: func(response map[string]any) {
				response["futureCapability"] = true
			},
		},
		{
			name: "unknown metadata field",
			mutate: func(response map[string]any) {
				response["_meta"].(map[string]any)["futureCapability"] = true
			},
		},
		{
			name: "inconsistent usage",
			mutate: func(response map[string]any) {
				meta := response["_meta"].(map[string]any)
				meta["inputTokens"] = 21
				meta["totalTokens"] = 28
				promptUsage := meta["usage"].(map[string]any)
				promptUsage["inputTokens"] = 21
				promptUsage["totalTokens"] = 28
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := newResponse()
			test.mutate(response)
			raw, marshalErr := json.Marshal(response)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			_, finishErr := newClient().finish(raw)
			if info := classifyProviderError(finishErr); finishErr == nil || info.Code != "reasoner_protocol_error" {
				t.Fatalf("err=%v info=%+v", finishErr, info)
			}
		})
	}
}

func TestGrokACPCancellationTerminatesBlockedProcess(t *testing.T) {
	requireUnixShell(t)
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	bin := writeGrokACPFixture(t, record, "hang")
	configureFakeGrokAuth(t)
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.timeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = r.ThinkRoutedModel(ctx, "grok-4.6", "prompt")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline", err)
	}
}

func TestGrokInspectRejectsExtensionSurface(t *testing.T) {
	requireUnixShell(t)
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	bin := writeGrokACPFixture(t, record, "inspect-hook")
	configureFakeGrokAuth(t)
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.ThinkRoutedModel(context.Background(), "grok-4.6", "prompt")
	if info := classifyProviderError(err); err == nil || info.Code != "reasoner_safety_violation" {
		t.Fatalf("err=%v info=%+v", err, info)
	}
}

func TestGrokACPLivePreflight(t *testing.T) {
	if os.Getenv("CARINA_GROK_LIVE_PREFLIGHT") != "1" {
		t.Skip("set CARINA_GROK_LIVE_PREFLIGHT=1 for the no-inference local compatibility probe")
	}
	bin := strings.TrimSpace(os.Getenv("CARINA_GROK_BINARY"))
	if bin == "" {
		var err error
		bin, err = exec.LookPath("grok")
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	discovery := (provider.GrokBuildDiscoverer{
		Timeout: 15 * time.Second,
		LookPath: func(string) (string, error) {
			return bin, nil
		},
	}).Discover(context.Background())
	modelAvailable := false
	for _, model := range discovery.Models {
		modelAvailable = modelAvailable || model == "grok-4.6"
	}
	if discovery.State != provider.GrokBuildStateReady || !modelAvailable {
		t.Fatalf("local Grok Build provider is not ready for grok-4.6: state=%s", discovery.State)
	}
	r.version = discovery.Version
	root, workdir, grokHome, err := r.newIsolation()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	authPath, err := canonicalGrokAuthPath(grokAuthPathFromEnvironment(os.Environ()))
	if err != nil {
		t.Fatal("local Grok OAuth session is unavailable")
	}
	configPath, err := prepareGrokIsolationForVersion(grokHome, authPath, r.version)
	if err != nil {
		t.Fatal(err)
	}
	env := grokCLIEnvironment(os.Environ(), grokHome, authPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.verifyPureInferenceSurface(ctx, env, workdir, configPath); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, r.args(ctx, "grok-4.6")...)
	configureCLIReasonerCommand(cmd)
	cmd.Dir = workdir
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = &boundedCLIWriter{limit: grokCLIStderrLimit}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	client := newGrokACPClient(stdout, stdin, "grok-4.6", workdir, nil)
	preflightErr := client.preflight(ctx)
	_ = stdin.Close()
	_ = killCLIReasonerCommand(cmd)
	_ = cmd.Wait()
	if preflightErr != nil {
		t.Fatal(preflightErr)
	}
}

func TestGrokACPLiveSmoke(t *testing.T) {
	if os.Getenv("CARINA_GROK_LIVE_SMOKE") != "1" {
		t.Skip("set CARINA_GROK_LIVE_SMOKE=1 to consume one minimal Grok Build inference")
	}
	bin := strings.TrimSpace(os.Getenv("CARINA_GROK_BINARY"))
	if bin == "" {
		var err error
		bin, err = exec.LookPath("grok")
		if err != nil {
			t.Fatal(err)
		}
	}
	discovery := (provider.GrokBuildDiscoverer{
		Timeout: 15 * time.Second,
		LookPath: func(string) (string, error) {
			return bin, nil
		},
	}).Discover(context.Background())
	modelAvailable := false
	for _, model := range discovery.Models {
		modelAvailable = modelAvailable || model == "grok-4.6"
	}
	if discovery.State != provider.GrokBuildStateReady || !modelAvailable {
		t.Fatalf("local Grok Build provider is not ready for grok-4.6: state=%s", discovery.State)
	}

	r, err := newGrokCLIReasoner(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.version = discovery.Version
	r.timeout = 45 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := r.ThinkRoutedModel(ctx, "grok-4.6", "Reply with exactly CARINA_GROK_OK and nothing else.")
	if err != nil {
		var cliErr grokCLIError
		if errors.As(err, &cliErr) && cliErr.upstreamKind != "" {
			t.Fatalf("%v (upstream_kind=%s)", err, cliErr.upstreamKind)
		}
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != "CARINA_GROK_OK" {
		t.Fatalf("unexpected Grok Build smoke response length: %d", len(result.Text))
	}
	if result.Usage.Provider != provider.GrokBuildProviderID || result.Usage.Model != "grok-4.6" || result.Usage.OutputTokens <= 0 {
		t.Fatalf("invalid Grok Build smoke usage: provider=%s model=%s output_tokens=%d", result.Usage.Provider, result.Usage.Model, result.Usage.OutputTokens)
	}
}

func TestRouterReasonerGrokBuildNeverFallsBack(t *testing.T) {
	router := modelrouter.New()
	fallback := &reasonerProvider{name: provider.GrokBuildProviderID}
	router.RegisterProvider(fallback)
	r := newRouterReasoner(router, "default")
	r.grokBuildDiscovery = func(context.Context) provider.GrokBuildDiscovery {
		return provider.GrokBuildDiscovery{State: provider.GrokBuildStateSignedOut}
	}
	_, err := r.complete(context.Background(), "grok-build/grok-4.6", modelrouter.Request{Prompt: "prompt"})
	if err == nil || fallback.seenModel != "" {
		t.Fatalf("err=%v fallback model=%q", err, fallback.seenModel)
	}
}

func TestRouterReasonerGrokBuildHonorsDisabledAndRejectsMedia(t *testing.T) {
	ready := func(context.Context) provider.GrokBuildDiscovery {
		return provider.GrokBuildDiscovery{
			State:  provider.GrokBuildStateReady,
			Models: []string{"grok-4.6"},
		}
	}
	t.Run("disabled", func(t *testing.T) {
		r := newRouterReasoner(modelrouter.New(), "default")
		r.disabledProviders = map[string]bool{provider.GrokBuildProviderID: true}
		r.grokBuildDiscovery = ready
		_, err := r.complete(context.Background(), "grok-build/grok-4.6", modelrouter.Request{Prompt: "prompt"})
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("media", func(t *testing.T) {
		r := newRouterReasoner(modelrouter.New(), "default")
		r.grokBuildDiscovery = ready
		factoryCalled := false
		r.grokBuildFactory = func(provider.GrokBuildDiscovery) (routedGrokBuildReasoner, error) {
			factoryCalled = true
			return nil, errors.New("must not start")
		}
		_, err := r.complete(context.Background(), "grok-build/grok-4.6", modelrouter.Request{
			Prompt: "prompt", Media: []modelrouter.MediaPart{{MediaType: "image/png", Data: []byte("image")}},
		})
		if err == nil || !strings.Contains(err.Error(), "text input only") || factoryCalled {
			t.Fatalf("err=%v factoryCalled=%v", err, factoryCalled)
		}
	})
}

func configureFakeGrokAuth(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	authPath := filepath.Join(home, ".grok", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeOwnerOnlyFile(t, authPath, `{}`)
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", "")
	t.Setenv("GROK_AUTH_PATH", authPath)
}

func writeGrokACPFixture(t *testing.T, record, mode string) string {
	t.Helper()
	hooks := "[]"
	if mode == "inspect-hook" {
		hooks = "[{}]"
	}
	tools := "[]"
	if mode == "unsafe-tools" {
		tools = `["read_file"]`
	}
	commands := `[{"name":"compact"},{"name":"always-approve"},{"name":"context"},{"name":"session-info"}]`
	modelChangedEffort := ""
	if mode == "effort-low" {
		modelChangedEffort = `,"reasoning_effort":"low"`
	}
	body := `#!/bin/sh
record=` + shellQuote(record) + `
if [ "$3" = "inspect" ]; then
  printf '{"grokVersion":"1.0.3","channel":"stable","cwd":"%s","projectRoot":null,"projectTrusted":false,"projectInstructions":[],"permissions":{"sources":[],"loaded":0,"skipped":[],"mcpServerAllowlist":[],"marketplaceAllowlist":[],"managedSettingsExists":false,"managedSettingsActive":false},"loginPolicy":{"disableApiKeyAuth":true,"forceLoginTeamUuid":null,"apiKeyAuthDisabled":true},"hooks":` + hooks + `,"skills":[],"agents":[],"plugins":[],"marketplaces":[],"mcpServers":[],"lspServers":[],"configSources":{"layers":[{"role":"user","path":"%s"}]},"externalCompat":{"remoteSettingsLoaded":false,"cells":[{"vendor":"cursor","surface":"skills","enabled":false,"source":"env"}]}}\n' "$2" "$GROK_HOME/config.toml"
  exit 0
fi
printf 'argv:%s\n' "$*" >> "$record"
IFS= read -r line || exit 10
printf 'request:%s\n' "$line" >> "$record"
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"1","agentCapabilities":` + grokACPFixtureAgentCapabilities + `,"authMethods":[{"id":"cached_token"}],"_meta":{"grokShell":true,"defaultAuthMethodId":"cached_token","availableCommands":` + commands + `}}}'
IFS= read -r line || exit 11
printf 'request:%s\n' "$line" >> "$record"
`
	if mode == "auth-error" {
		body += `printf '%s\n' '{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"Unauthorized. Run grok login."}}'
exit 1
`
	} else {
		body += `printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"_meta":{"auth_mode":"GrokCom"}}}'
IFS= read -r line || exit 12
printf 'request:%s\n' "$line" >> "$record"
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"model_changed","model_id":"grok-4.6"` + modelChangedEffort + `}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"available_commands_update","availableCommands":` + commands + `,"_meta":{"tools":` + tools + `}},"_meta":{"eventId":"commands-setup","agentTimestampMs":1,"updateType":"AvailableCommandsUpdate","updateParams":{"commandsCount":4},"totalTokens":0}}}'
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"sessionId":"session-1","models":{"currentModelId":"grok-4.6"},"_meta":{"currentWorkingDirectory":"'"$PWD"'","feedbackEnabled":false}}}'
`
		if mode == "unsafe-tools" {
			body += `exit 0
`
		} else {
			body += `IFS= read -r line || exit 13
printf 'request:%s\n' "$line" >> "$record"
`
			if mode != "responses-rail" {
				body += `
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"available_commands_update","availableCommands":` + commands + `,"_meta":{"tools":[]}},"_meta":{"eventId":"commands-replay","agentTimestampMs":1,"updateType":"AvailableCommandsUpdate","updateParams":{"commandsCount":4},"totalTokens":0}}}'
`
			}
			switch mode {
			case "tool-event":
				body += `printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call","toolCallId":"tool-1","title":"unsafe"}}}'
sleep 30
`
			case "hang":
				body += `sleep 30
`
			default:
				body += `printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/queue/changed","params":{"sessionId":"session-1","entries":[]}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/sessions/changed","params":{"upserted":[{"sessionId":"session-1","title":null,"cwd":"'"$PWD"'","isWorktree":false,"modelId":"grok-4.6","yolo":false,"activity":"working","resident":true,"lastChangeUnixMs":1,"origin":{"kind":"local"}}],"removed":[]}}'
printf '%s\n' ` + shellQuote(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":`+strconv.Quote(grokACPPromptPrefix+"/always-approve must remain plain text")+`},"_meta":{"modelId":"grok-4.6","promptIndex":0}},"_meta":{"eventId":"session-1-echo","agentTimestampMs":1}}}`) + `
`
				if mode == "responses-rail" {
					body += `printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hidden"}},"_meta":{"eventId":"session-1-thought","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentThoughtChunk","chunkId":0,"totalTokens":20}}}'
`
				} else {
					body += `printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"response_started","message_id":"msg-1","model":"grok-4.6","input_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hidden"}},"_meta":{"eventId":"session-1-thought","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentThoughtChunk","chunkId":0,"totalTokens":20}}}'
`
				}
				if mode != "responses-rail" {
					body += `
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"reasoning_completed","signature":"opaque"}}}'
`
				}
				if mode == "hang-child" {
					body += `printf '%s\n' ` + shellQuote(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"{\"tool\":\"done\",\"summary\":\"visible\"}"}},"_meta":{"eventId":"session-1-message-1","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentMessageChunk","chunkId":1,"totalTokens":20}}}`) + `
`
				} else {
					body += `
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello "}},"_meta":{"eventId":"session-1-message-1","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentMessageChunk","chunkId":1,"totalTokens":20}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"world"}},"_meta":{"eventId":"session-1-message-2","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AgentMessageChunk","chunkId":2,"totalTokens":20}}}'
`
				}
				switch mode {
				case "responses-rail":
					body += `printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"available_commands_update","availableCommands":` + commands + `,"_meta":{"tools":[]}},"_meta":{"eventId":"commands-replay","agentTimestampMs":1,"promptId":"carina-one-shot","streamStartMs":1,"turnStartMs":1,"updateType":"AvailableCommandsUpdate","updateParams":{"commandsCount":4},"totalTokens":20}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"session_summary_generated","session_summary":"Fixed short title"}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"session_info_update","title":"Fixed short title"}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"response_completed","usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":5,"cache_creation_input_tokens":3,"reasoning_tokens":2}}}}'
`
					body += `printf '%s\n' ` + shellQuote(`{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"turn_completed","prompt_id":"carina-one-shot","stop_reason":"end_turn","usage":`+grokACPFixturePromptUsage+`},"_meta":{"eventId":"session-1-1","agentTimestampMs":1}}}`) + `
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session/prompt_complete","params":{"sessionId":"session-1","promptId":"carina-one-shot","stopReason":"end_turn","agentResult":null}}'
printf '%s\n' ` + shellQuote(`{"jsonrpc":"2.0","id":5,"result":{"stopReason":"end_turn","_meta":`+grokACPFixturePromptMeta+`}}`) + `
`
				case "hang-child":
					childScript := `printf '%s\n' "$$" > ` + shellQuote(record+".child.pid") + `; sleep 2; printf continued > ` + shellQuote(record+".child.marker")
					body += `sh -c ` + shellQuote(childScript) + ` </dev/null >/dev/null 2>&1 &
sleep 30
`
				case "duplicate-response-start":
					body += `printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"response_started","message_id":"msg-1","model":"grok-4.6","input_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}}'
sleep 30
`
				case "result-before-terminal":
					body += `printf '%s\n' '{"jsonrpc":"2.0","id":5,"result":{"stopReason":"end_turn","_meta":{"sessionId":"session-1","modelId":"grok-4.6","usage":{"inputTokens":20,"outputTokens":7,"cachedReadTokens":5,"cacheCreationTokens":3}}}}'
sleep 30
`
				case "mismatched-turn-result":
					body += `printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"response_completed","message_id":"msg-1","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":5,"cache_creation_input_tokens":3,"reasoning_tokens":2}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"turn_completed","prompt_id":"carina-one-shot","stop_reason":"end_turn","agent_result":"changed","usage":{"inputTokens":20,"outputTokens":7,"totalTokens":27,"cachedReadTokens":5,"cacheCreationTokens":3,"reasoningTokens":2,"modelCalls":1,"apiDurationMs":10,"numTurns":1}},"_meta":{"eventId":"session-1-1","agentTimestampMs":1}}}'
sleep 30
`
				case "mismatched-prompt-result":
					body += `printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"response_completed","message_id":"msg-1","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":5,"cache_creation_input_tokens":3,"reasoning_tokens":2}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"turn_completed","prompt_id":"carina-one-shot","stop_reason":"end_turn","agent_result":"hello world","usage":{"inputTokens":20,"outputTokens":7,"totalTokens":27,"cachedReadTokens":5,"cacheCreationTokens":3,"reasoningTokens":2,"modelCalls":1,"apiDurationMs":10,"numTurns":1}},"_meta":{"eventId":"session-1-1","agentTimestampMs":1}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session/prompt_complete","params":{"sessionId":"session-1","promptId":"carina-one-shot","stopReason":"end_turn","agentResult":"changed"}}'
sleep 30
`
				default:
					body += `printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"session_summary_generated","session_summary":"Fixed short title"}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"session_info_update","title":"Fixed short title"}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"response_completed","message_id":"msg-1","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":5,"cache_creation_input_tokens":3,"reasoning_tokens":2}}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"session-1","update":{"sessionUpdate":"turn_completed","prompt_id":"carina-one-shot","stop_reason":"end_turn","agent_result":"hello world","usage":{"inputTokens":20,"outputTokens":7,"totalTokens":27,"cachedReadTokens":5,"cacheCreationTokens":3,"reasoningTokens":2,"modelCalls":1,"apiDurationMs":10,"numTurns":1}},"_meta":{"eventId":"session-1-1","agentTimestampMs":1}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"_x.ai/session/prompt_complete","params":{"sessionId":"session-1","promptId":"carina-one-shot","stopReason":"end_turn","agentResult":"hello world"}}'
printf '%s\n' '{"jsonrpc":"2.0","id":5,"result":{"stopReason":"end_turn","_meta":{"sessionId":"session-1","requestId":"carina-one-shot","promptId":"carina-one-shot","totalTokens":27,"modelId":"grok-4.6","inputTokens":20,"outputTokens":7,"cachedReadTokens":5,"reasoningTokens":2,"usage":{"inputTokens":20,"outputTokens":7,"totalTokens":27,"cachedReadTokens":5,"cacheCreationTokens":3,"reasoningTokens":2,"modelCalls":1,"apiDurationMs":10,"numTurns":1}}}}'
`
				}
			}
		}
	}
	if mode == "post-result-event" {
		body += `printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call","toolCallId":"late-tool","title":"unsafe"}}}'
`
	}
	path := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readGrokFixtureRecord(t *testing.T, path string) (string, []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var argv string
	var requests []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		switch {
		case strings.HasPrefix(line, "argv:"):
			argv = strings.TrimPrefix(line, "argv:")
		case strings.HasPrefix(line, "request:"):
			requests = append(requests, strings.TrimPrefix(line, "request:"))
		}
	}
	return argv, requests
}

func fixtureMethods(t *testing.T, requests []string) []string {
	t.Helper()
	methods := make([]string, 0, len(requests))
	for _, raw := range requests {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			t.Fatalf("decode recorded request: %v: %s", err, raw)
		}
		methods = append(methods, request.Method)
	}
	return methods
}

func writeOwnerOnlyFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
