package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// scrubCarinaEnv unsets every CARINA_* variable for the duration of the test
// so hermetic tests are not polluted by the host or CI job environment
// (e.g. the CI go-test step exports CARINA_KERNEL_BIN for the E2E suite).
func scrubCarinaEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CARINA_") {
			continue
		}
		key, val, _ := strings.Cut(kv, "=")
		t.Setenv(key, val) // registers restore-on-cleanup
		os.Unsetenv(key)
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	cdir := filepath.Join(dir, ".carina")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCascadePrecedence: env > project > global > default, and absent keys fall
// through to the prior layer.
func TestCascadePrecedence(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	proj := t.TempDir()

	writeConfig(t, home, `{"offline": true, "max_task_tokens": 100, "tools_dir": "/g/tools", "gateway_http": "127.0.0.1:7000", "gateway_http_origins": ["https://api.example"], "gateway_ws": "127.0.0.1:7001", "gateway_ws_origins": ["https://app.example"], "gateway_token_signing_key_file": "/g/token.key", "gateway_token_max_ttl_seconds": 600, "enable_debug_rpc": true, "summarizer_model": "cheap", "risk_review_model": "guardian", "context_engine": "noop", "headroom_mode": "managed_mcp", "headroom_token_budget": 1234}`)
	writeConfig(t, proj, `{"max_task_tokens": 200, "tools_dir": "/p/tools", "risk_review_mode": "enforce", "headroom_mode": "proxy"}`)
	t.Setenv("CARINA_TOOLS_DIR", "/e/tools")
	t.Setenv("CARINA_RISK_REVIEW_MODE", "advisory")
	t.Setenv("CARINA_GATEWAY_TOKEN_MAX_TTL_SECONDS", "300")
	t.Setenv("CARINA_NEBUTRA_CLOUD_ENDPOINT", "https://nebutra.example")
	t.Setenv("CARINA_CONTEXT_ENGINE", "headroom")
	t.Setenv("CARINA_HEADROOM_PROXY_PORT", "7777")
	t.Setenv("CARINA_ENABLE_DEBUG_RPC", "false")

	cfg, err := Load(home, proj)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !cfg.Offline {
		t.Error("offline should come from the global layer (true)")
	}
	if cfg.MaxTaskTokens != 200 {
		t.Errorf("max_task_tokens: project should override global, want 200 got %d", cfg.MaxTaskTokens)
	}
	if cfg.ToolsDir != "/e/tools" {
		t.Errorf("tools_dir: env should override project, want /e/tools got %q", cfg.ToolsDir)
	}
	if cfg.GatewayWS != "127.0.0.1:7001" {
		t.Errorf("gateway_ws should fall through from global, got %q", cfg.GatewayWS)
	}
	if cfg.GatewayHTTP != "127.0.0.1:7000" {
		t.Errorf("gateway_http should fall through from global, got %q", cfg.GatewayHTTP)
	}
	if !reflect.DeepEqual(cfg.GatewayHTTPOrigins, []string{"https://api.example"}) {
		t.Errorf("gateway_http_origins should fall through from global, got %#v", cfg.GatewayHTTPOrigins)
	}
	if !reflect.DeepEqual(cfg.GatewayWSOrigins, []string{"https://app.example"}) {
		t.Errorf("gateway_ws_origins should fall through from global, got %#v", cfg.GatewayWSOrigins)
	}
	if cfg.GatewayTokenSigningKeyFile != "/g/token.key" {
		t.Errorf("gateway_token_signing_key_file should fall through from global, got %q", cfg.GatewayTokenSigningKeyFile)
	}
	if cfg.GatewayTokenMaxTTLSeconds != 300 {
		t.Errorf("gateway_token_max_ttl_seconds: env should override global, got %d", cfg.GatewayTokenMaxTTLSeconds)
	}
	if cfg.EnableDebugRPC {
		t.Errorf("enable_debug_rpc: env false should override global true")
	}
	if cfg.SummarizerModel != "cheap" {
		t.Errorf("summarizer_model should fall through from global, got %q", cfg.SummarizerModel)
	}
	if cfg.RiskReviewModel != "guardian" {
		t.Errorf("risk_review_model should fall through from global, got %q", cfg.RiskReviewModel)
	}
	if cfg.RiskReviewMode != "advisory" {
		t.Errorf("risk_review_mode: env should override project, got %q", cfg.RiskReviewMode)
	}
	if cfg.NebutraCloudEndpoint != "https://nebutra.example" {
		t.Errorf("nebutra_cloud_endpoint: env should override defaults, got %q", cfg.NebutraCloudEndpoint)
	}
	if cfg.NebutraSyncMode != "off" {
		t.Errorf("nebutra_sync_mode default should be off, got %q", cfg.NebutraSyncMode)
	}
	if cfg.ContextEngine != "headroom" {
		t.Errorf("context_engine: env should override global, got %q", cfg.ContextEngine)
	}
	if cfg.HeadroomMode != "proxy" {
		t.Errorf("headroom_mode: project should override global, got %q", cfg.HeadroomMode)
	}
	if cfg.HeadroomProxyPort != 7777 {
		t.Errorf("headroom_proxy_port: env should override defaults, got %d", cfg.HeadroomProxyPort)
	}
	if cfg.HeadroomTokenBudget != 1234 {
		t.Errorf("headroom_token_budget should fall through from global, got %d", cfg.HeadroomTokenBudget)
	}
	// A key set by no layer keeps its default.
	if cfg.MaxConcurrentTasks != 8 {
		t.Errorf("max_concurrent_tasks default should survive, got %d", cfg.MaxConcurrentTasks)
	}
	if cfg.StateDir != filepath.Join(home, ".carina", "state") {
		t.Errorf("state_dir default mismatch: %q", cfg.StateDir)
	}
}

// TestBestOfNEnabledCascade covers the config surface for the opt-in
// best_of_n tool: defaults to off, a config file can turn it on, and
// CARINA_BEST_OF_N_ENABLED overrides both.
func TestBestOfNEnabledCascade(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()

	cfg, err := Load(home, t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BestOfNEnabled {
		t.Fatal("best_of_n_enabled must default to false")
	}

	writeConfig(t, home, `{"best_of_n_enabled": true}`)
	cfg, err = Load(home, t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.BestOfNEnabled {
		t.Fatal("best_of_n_enabled: config file should turn it on")
	}

	t.Setenv("CARINA_BEST_OF_N_ENABLED", "false")
	cfg, err = Load(home, t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BestOfNEnabled {
		t.Fatal("best_of_n_enabled: env var should override the config file")
	}
}

func TestNoFilesYieldsDefaults(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	cfg, err := Load(home, t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(cfg, Defaults(home)) {
		t.Fatalf("with no files/env, config must equal defaults:\n got %+v\nwant %+v", cfg, Defaults(home))
	}
}

func TestMalformedFileIsHardError(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"offline": true,`) // truncated JSON
	if _, err := Load(home, ""); err == nil {
		t.Fatal("a malformed config file must fail fast, not be ignored")
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"typo_key": 1}`)
	if _, err := Load(home, ""); err == nil {
		t.Fatal("an unknown config key must be rejected (typo protection)")
	}
}

func TestEnvValidationFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARINA_MAX_TASK_TOKENS", "-5")
	if _, err := Load(home, ""); err == nil {
		t.Fatal("a negative token budget must be rejected")
	}
}

func TestGatewayTokenTTLValidationFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARINA_GATEWAY_TOKEN_MAX_TTL_SECONDS", "-1")
	if _, err := Load(home, ""); err == nil {
		t.Fatal("negative gateway token ttl must be rejected")
	}
}

func TestRiskReviewModeValidationFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARINA_RISK_REVIEW_MODE", "always")
	if _, err := Load(home, ""); err == nil {
		t.Fatal("invalid risk_review_mode must be rejected")
	}
}

func TestNebutraCloudValidationFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARINA_NEBUTRA_CLOUD_ENDPOINT", "http://nebutra.com")
	if _, err := Load(home, ""); err == nil {
		t.Fatal("non-local http Nebutra endpoint must be rejected")
	}
}

func TestNebutraSyncModeValidationFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARINA_NEBUTRA_SYNC_MODE", "metadata")
	if _, err := Load(home, ""); err == nil {
		t.Fatal("sync modes beyond off must be rejected until the Nebutra connector exists")
	}
}

func TestContextEngineValidationFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARINA_CONTEXT_ENGINE", "always")
	if _, err := Load(home, ""); err == nil {
		t.Fatal("invalid context engine must be rejected")
	}
}

func TestHMSMemoryConfigValidation(t *testing.T) {
	cfg := Defaults(t.TempDir())
	cfg.MemoryProvider = "hms-hybrid"
	cfg.MemoryHMSEndpoint = "https://hms.example"
	cfg.MemoryHMSAPIKeyEnv = "HMS_TOKEN"
	cfg.MemoryHMSBankKeyEnv = "HMS_BANK_KEY"
	cfg.MemoryHMSProjectionEnabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Config){
		"insecure remote endpoint": func(c *Config) { c.MemoryHMSEndpoint = "http://hms.example" },
		"credential in endpoint":   func(c *Config) { c.MemoryHMSEndpoint = "https://user:pass@hms.example" },
		"missing API key handle":   func(c *Config) { c.MemoryHMSAPIKeyEnv = "" },
		"missing bank key handle":  func(c *Config) { c.MemoryHMSBankKeyEnv = "" },
		"excess evidence":          func(c *Config) { c.MemoryHMSMaxEvidence = 51 },
		"unknown mode":             func(c *Config) { c.MemoryProvider = "hms-magic" },
		"invalid projection poll":  func(c *Config) { c.MemoryHMSProjectionPollMS = 99 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cfg
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid HMS config accepted")
			}
		})
	}
	cfg.MemoryHMSEndpoint = "http://127.0.0.1:18080"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("loopback development endpoint rejected: %v", err)
	}
	off := Defaults(t.TempDir())
	off.MemoryHMSProjectionEnabled = true
	if off.Validate() == nil {
		t.Fatal("projection without HMS provider accepted")
	}
}

func TestProjectCannotConfigureHMSOrSecretHandles(t *testing.T) {
	for _, key := range []string{"memory_provider", "memory_hms_endpoint", "memory_hms_api_key_env", "memory_hms_bank_key_env"} {
		t.Run(key, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			writeConfig(t, project, `{"`+key+`":"attacker-controlled"}`)
			if _, err := Load(home, project); err == nil || !strings.Contains(err.Error(), "deployment-owned") {
				t.Fatalf("project HMS config was not rejected: %v", err)
			}
		})
	}
}

func TestHeadroomModeValidationFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARINA_HEADROOM_MODE", "http")
	if _, err := Load(home, ""); err == nil {
		t.Fatal("invalid headroom mode must be rejected")
	}
}

func TestHeadroomBudgetValidationFailsFast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARINA_HEADROOM_TOKEN_BUDGET", "-1")
	if _, err := Load(home, ""); err == nil {
		t.Fatal("negative headroom token budget must be rejected")
	}
}

func TestTUIKeybindingsMergeAcrossGlobalAndProjectConfig(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	project := t.TempDir()
	writeConfig(t, home, `{"tui_keybindings":{"global.help":["ctrl+h"]}}`)
	writeConfig(t, project, `{"tui_keybindings":{"composer.submit":["ctrl+enter"]}}`)
	cfg, err := Load(home, project)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"global.help":     {"ctrl+h"},
		"composer.submit": {"ctrl+enter"},
	}
	if !reflect.DeepEqual(cfg.TUIKeybindings, want) {
		t.Fatalf("tui keybindings = %#v, want %#v", cfg.TUIKeybindings, want)
	}
}

func TestTUIKeybindingSameActionMayOverrideAcrossConfigLayers(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	project := t.TempDir()
	writeConfig(t, home, `{"tui_keybindings":{"global.help":["f2"]}}`)
	writeConfig(t, project, `{"tui_keybindings":{"global.help":["f3"]}}`)

	cfg, err := Load(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.TUIKeybindings["global.help"]; !reflect.DeepEqual(got, []string{"f3"}) {
		t.Fatalf("project binding = %#v, want project layer override", got)
	}
}

func TestConfigRejectsDuplicateJSONKeys(t *testing.T) {
	tests := []struct {
		name      string
		contents  string
		duplicate string
		jsonPath  string
	}{
		{name: "root", contents: `{"offline":true,"offline":false}`, duplicate: "offline", jsonPath: "$"},
		{name: "tui action", contents: `{"tui_keybindings":{"global.help":["f1"],"global.help":["f2"]}}`, duplicate: "global.help", jsonPath: `$["tui_keybindings"]`},
		{name: "nested object", contents: `{"tui_keybindings":{"global.help":["f1"]},"extra":{"value":1,"value":2}}`, duplicate: "value", jsonPath: `$["extra"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".carina", "config.json")
			writeConfig(t, home, tt.contents)
			_, err := Load(home, "")
			if err == nil {
				t.Fatal("duplicate JSON key must be rejected")
			}
			for _, want := range []string{path, tt.duplicate, tt.jsonPath, "remove one"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestDuplicateJSONKeyScannerIgnoresStringContentsAndSeparateObjects(t *testing.T) {
	data := []byte(`{"description":"fake key: \"offline\": true, \"offline\": false","items":[{"name":"same"},{"name":"same"}]}`)
	if err := rejectDuplicateJSONKeys(data); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
}

func TestTUILocaleLayersThroughDedicatedEnvironmentVariable(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	project := t.TempDir()
	writeConfig(t, home, `{"tui_locale":"ja-JP"}`)
	writeConfig(t, project, `{"tui_locale":"es-ES"}`)

	cfg, err := Load(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TUILocale != "es-ES" {
		t.Fatalf("project tui locale = %q, want es-ES", cfg.TUILocale)
	}

	t.Setenv("CARINA_TUI_LOCALE", "ko-KR")
	cfg, err = Load(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TUILocale != "ko-KR" {
		t.Fatalf("environment tui locale = %q, want ko-KR", cfg.TUILocale)
	}
}

func TestTUILocaleRejectsUnsupportedExplicitValues(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	writeConfig(t, home, `{"tui_locale":"zh-TW"}`)
	cfg, err := Load(home, t.TempDir())
	if err != nil {
		t.Fatalf("zh-TW tui_locale should be accepted as zh-Hant, got %v", err)
	}
	// Config stores the raw value; CanonicalLocale is applied by launchers.
	if cfg.TUILocale != "zh-TW" {
		t.Fatalf("tui_locale = %q, want zh-TW raw config value", cfg.TUILocale)
	}
	// Unsupported language still fails.
	writeConfig(t, home, `{"tui_locale":"de-DE"}`)
	if _, err := Load(home, t.TempDir()); !errors.Is(err, ErrInvalidTUILocale) {
		t.Fatal("unsupported config tui_locale must fail")
	}

	home = t.TempDir()
	t.Setenv("CARINA_TUI_LOCALE", "de-DE")
	if _, err := Load(home, t.TempDir()); !errors.Is(err, ErrInvalidTUILocale) {
		t.Fatal("unsupported CARINA_TUI_LOCALE must fail")
	}
}

func TestTUIAlternateScreenLayersAndValidates(t *testing.T) {
	scrubCarinaEnv(t)
	home := t.TempDir()
	project := t.TempDir()
	writeConfig(t, home, `{"tui_alternate_screen":"never"}`)
	cfg, err := Load(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TUIAlternateScreen != "never" {
		t.Fatalf("tui alternate screen = %q, want never", cfg.TUIAlternateScreen)
	}

	t.Setenv("CARINA_TUI_ALTERNATE_SCREEN", "always")
	cfg, err = Load(home, project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TUIAlternateScreen != "always" {
		t.Fatalf("env tui alternate screen = %q, want always", cfg.TUIAlternateScreen)
	}

	t.Setenv("CARINA_TUI_ALTERNATE_SCREEN", "sometimes")
	if _, err := Load(home, project); err == nil {
		t.Fatal("invalid tui alternate screen mode must be rejected")
	}
}
