package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
	home := t.TempDir()
	proj := t.TempDir()

	writeConfig(t, home, `{"offline": true, "max_task_tokens": 100, "tools_dir": "/g/tools", "gateway_ws": "127.0.0.1:7001", "gateway_ws_origins": ["https://app.example"], "gateway_token_signing_key_file": "/g/token.key", "gateway_token_max_ttl_seconds": 600, "summarizer_model": "cheap", "risk_review_model": "guardian"}`)
	writeConfig(t, proj, `{"max_task_tokens": 200, "tools_dir": "/p/tools", "risk_review_mode": "enforce"}`)
	t.Setenv("CARINA_TOOLS_DIR", "/e/tools")
	t.Setenv("CARINA_RISK_REVIEW_MODE", "advisory")
	t.Setenv("CARINA_GATEWAY_TOKEN_MAX_TTL_SECONDS", "300")
	t.Setenv("CARINA_NEBUTRA_CLOUD_ENDPOINT", "https://nebutra.example")

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
	if !reflect.DeepEqual(cfg.GatewayWSOrigins, []string{"https://app.example"}) {
		t.Errorf("gateway_ws_origins should fall through from global, got %#v", cfg.GatewayWSOrigins)
	}
	if cfg.GatewayTokenSigningKeyFile != "/g/token.key" {
		t.Errorf("gateway_token_signing_key_file should fall through from global, got %q", cfg.GatewayTokenSigningKeyFile)
	}
	if cfg.GatewayTokenMaxTTLSeconds != 300 {
		t.Errorf("gateway_token_max_ttl_seconds: env should override global, got %d", cfg.GatewayTokenMaxTTLSeconds)
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
	// A key set by no layer keeps its default.
	if cfg.MaxConcurrentTasks != 8 {
		t.Errorf("max_concurrent_tasks default should survive, got %d", cfg.MaxConcurrentTasks)
	}
	if cfg.StateDir != filepath.Join(home, ".carina", "state") {
		t.Errorf("state_dir default mismatch: %q", cfg.StateDir)
	}
}

func TestNoFilesYieldsDefaults(t *testing.T) {
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
