// Package config resolves daemon configuration from a layered cascade, later
// layers overriding earlier ones:
//
//  1. built-in defaults
//  2. managed  /etc/carina/managed.json (org-managed values, optional)
//  3. global   ~/.carina/config.json
//  4. project  <projectDir>/.carina/config.json
//  5. environment (CARINA_*)
//
// Keys named in the managed file's locked_keys list are then re-applied from
// the managed values, so layers 3-5 cannot override them (tighten-only).
//
// The caller (the daemon entrypoint) applies command-line flags as a final,
// highest-precedence layer on top of the resolved config; the daemon
// entrypoint fails closed when an explicitly-set flag collides with a lock.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nebutra/carina/go/contextengine"
	"github.com/Nebutra/carina/go/nebutra"
)

// Config is the resolved daemon configuration. JSON tags name the keys accepted
// in the config files.
type Config struct {
	StateDir                   string              `json:"state_dir"`
	Socket                     string              `json:"socket"`
	TCP                        string              `json:"tcp"`
	GatewayHTTP                string              `json:"gateway_http"`
	GatewayHTTPOrigins         []string            `json:"gateway_http_origins"`
	GatewayWS                  string              `json:"gateway_ws"`
	GatewayWSOrigins           []string            `json:"gateway_ws_origins"`
	GatewayTokenSigningKeyFile string              `json:"gateway_token_signing_key_file"`
	GatewayTokenMaxTTLSeconds  int                 `json:"gateway_token_max_ttl_seconds"`
	KernelBin                  string              `json:"kernel_bin"`
	ToolsDir                   string              `json:"tools_dir"`
	PolicyDir                  string              `json:"policy_dir"`
	Offline                    bool                `json:"offline"`
	MaxConcurrentTasks         int                 `json:"max_concurrent_tasks"`
	RequireWorkspaceTrust      bool                `json:"require_workspace_trust"`
	MaxTaskTokens              int                 `json:"max_task_tokens"`
	EnableEgressProxy          bool                `json:"enable_egress_proxy"`
	EgressAllow                []string            `json:"egress_allow"`
	SandboxCommands            bool                `json:"sandbox_commands"`
	InteractiveApproval        bool                `json:"interactive_approval"`
	EnableDebugRPC             bool                `json:"enable_debug_rpc"`
	BestOfNEnabled             bool                `json:"best_of_n_enabled"`
	SummarizerModel            string              `json:"summarizer_model"`
	RiskReviewMode             string              `json:"risk_review_mode"`
	RiskReviewModel            string              `json:"risk_review_model"`
	NebutraCloudEndpoint       string              `json:"nebutra_cloud_endpoint"`
	NebutraSyncMode            string              `json:"nebutra_sync_mode"`
	ContextEngine              string              `json:"context_engine"`
	HeadroomBin                string              `json:"headroom_bin"`
	HeadroomStateDir           string              `json:"headroom_state_dir"`
	HeadroomMode               string              `json:"headroom_mode"`
	HeadroomProxyPort          int                 `json:"headroom_proxy_port"`
	HeadroomTokenBudget        int                 `json:"headroom_token_budget"`
	TUIKeybindings             map[string][]string `json:"tui_keybindings"`
}

// Defaults returns the built-in baseline, anchored at the user's ~/.carina dir.
func Defaults(home string) Config {
	base := filepath.Join(home, ".carina")
	return Config{
		StateDir:                  filepath.Join(base, "state"),
		Socket:                    filepath.Join(base, "daemon.sock"),
		PolicyDir:                 filepath.Join(base, "policy"),
		MaxConcurrentTasks:        8,
		GatewayTokenMaxTTLSeconds: 900,
		NebutraCloudEndpoint:      nebutra.DefaultCloudEndpoint,
		NebutraSyncMode:           nebutra.SyncModeOff,
		ContextEngine:             contextengine.ModeAuto,
		HeadroomMode:              contextengine.HeadroomModeManagedMCP,
		HeadroomTokenBudget:       4000,
	}
}

// Load resolves the config cascade using the platform-default managed path.
// Missing files are skipped; a malformed file is a hard error (fail fast
// rather than silently run mis-configured).
func Load(home, projectDir string) (Config, error) {
	cfg, _, err := LoadWithManaged(home, projectDir, DefaultManagedPath())
	return cfg, err
}

// LoadWithManaged resolves the config cascade with an explicit managed-file
// path ("" disables the managed layer). The returned LockReport is nil when no
// managed file is present; otherwise it names the locked keys and their source
// for provenance in errors and logs.
func LoadWithManaged(home, projectDir, managedPath string) (Config, *LockReport, error) {
	cfg := Defaults(home)
	var managed *Managed
	if managedPath != "" {
		m, err := loadManaged(managedPath)
		if err != nil {
			return cfg, nil, err
		}
		managed = m
	}
	if managed != nil {
		if err := managed.apply(&cfg, managedPath); err != nil {
			return cfg, nil, err
		}
	}
	if err := mergeFile(&cfg, filepath.Join(home, ".carina", "config.json")); err != nil {
		return cfg, nil, err
	}
	if projectDir != "" {
		if err := mergeFile(&cfg, filepath.Join(projectDir, ".carina", "config.json")); err != nil {
			return cfg, nil, err
		}
	}
	mergeEnv(&cfg)
	var report *LockReport
	if managed != nil {
		if err := managed.applyLocked(&cfg, managedPath); err != nil {
			return cfg, nil, err
		}
		report = managed.report(managedPath)
	}
	return cfg, report, cfg.Validate()
}

// mergeFile overlays a JSON file onto cfg. Unmarshaling into the existing struct
// only touches keys present in the file, so absent keys keep the prior layer's
// value.
func mergeFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	return nil
}

// mergeEnv overlays CARINA_* environment variables (the highest cascade layer,
// below command-line flags).
func mergeEnv(cfg *Config) {
	envStr("CARINA_STATE_DIR", &cfg.StateDir)
	envStr("CARINA_SOCKET", &cfg.Socket)
	envStr("CARINA_TCP", &cfg.TCP)
	envStr("CARINA_GATEWAY_HTTP", &cfg.GatewayHTTP)
	envList("CARINA_GATEWAY_HTTP_ORIGINS", &cfg.GatewayHTTPOrigins)
	envStr("CARINA_GATEWAY_WS", &cfg.GatewayWS)
	envList("CARINA_GATEWAY_WS_ORIGINS", &cfg.GatewayWSOrigins)
	envStr("CARINA_GATEWAY_TOKEN_SIGNING_KEY_FILE", &cfg.GatewayTokenSigningKeyFile)
	envInt("CARINA_GATEWAY_TOKEN_MAX_TTL_SECONDS", &cfg.GatewayTokenMaxTTLSeconds)
	envStr("CARINA_KERNEL_BIN", &cfg.KernelBin)
	envStr("CARINA_TOOLS_DIR", &cfg.ToolsDir)
	envStr("CARINA_POLICY_DIR", &cfg.PolicyDir)
	envStr("CARINA_SUMMARIZER_MODEL", &cfg.SummarizerModel)
	envStr("CARINA_RISK_REVIEW_MODE", &cfg.RiskReviewMode)
	envStr("CARINA_RISK_REVIEW_MODEL", &cfg.RiskReviewModel)
	envStr("CARINA_NEBUTRA_CLOUD_ENDPOINT", &cfg.NebutraCloudEndpoint)
	envStr("CARINA_NEBUTRA_SYNC_MODE", &cfg.NebutraSyncMode)
	envStr("CARINA_CONTEXT_ENGINE", &cfg.ContextEngine)
	envStr("CARINA_HEADROOM_BIN", &cfg.HeadroomBin)
	envStr("CARINA_HEADROOM_STATE_DIR", &cfg.HeadroomStateDir)
	envStr("CARINA_HEADROOM_MODE", &cfg.HeadroomMode)
	envBool("CARINA_OFFLINE", &cfg.Offline)
	envBool("CARINA_REQUIRE_WORKSPACE_TRUST", &cfg.RequireWorkspaceTrust)
	envBool("CARINA_ENABLE_EGRESS_PROXY", &cfg.EnableEgressProxy)
	envBool("CARINA_SANDBOX_COMMANDS", &cfg.SandboxCommands)
	envBool("CARINA_INTERACTIVE_APPROVAL", &cfg.InteractiveApproval)
	envBool("CARINA_ENABLE_DEBUG_RPC", &cfg.EnableDebugRPC)
	envBool("CARINA_BEST_OF_N_ENABLED", &cfg.BestOfNEnabled)
	envInt("CARINA_MAX_CONCURRENT_TASKS", &cfg.MaxConcurrentTasks)
	envInt("CARINA_MAX_TASK_TOKENS", &cfg.MaxTaskTokens)
	envInt("CARINA_HEADROOM_PROXY_PORT", &cfg.HeadroomProxyPort)
	envInt("CARINA_HEADROOM_TOKEN_BUDGET", &cfg.HeadroomTokenBudget)
	envList("CARINA_EGRESS_ALLOW", &cfg.EgressAllow)
}

// Validate rejects nonsensical values (fail fast at startup).
func (c Config) Validate() error {
	if c.MaxTaskTokens < 0 {
		return fmt.Errorf("config: max_task_tokens must be >= 0, got %d", c.MaxTaskTokens)
	}
	if c.MaxConcurrentTasks < 0 {
		return fmt.Errorf("config: max_concurrent_tasks must be >= 0, got %d", c.MaxConcurrentTasks)
	}
	if c.GatewayTokenMaxTTLSeconds < 0 {
		return fmt.Errorf("config: gateway_token_max_ttl_seconds must be >= 0, got %d", c.GatewayTokenMaxTTLSeconds)
	}
	if mode := strings.ToLower(strings.TrimSpace(c.RiskReviewMode)); mode != "" && mode != "off" && mode != "advisory" && mode != "enforce" {
		return fmt.Errorf("config: risk_review_mode must be one of off, advisory, enforce")
	}
	if _, err := nebutra.NormalizeCloudEndpoint(c.NebutraCloudEndpoint); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if _, err := nebutra.NormalizeSyncMode(c.NebutraSyncMode); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if _, err := contextengine.NormalizeConfig(contextengine.Config{
		ContextEngine:       c.ContextEngine,
		HeadroomBin:         c.HeadroomBin,
		HeadroomStateDir:    c.HeadroomStateDir,
		HeadroomMode:        c.HeadroomMode,
		HeadroomProxyPort:   c.HeadroomProxyPort,
		HeadroomTokenBudget: c.HeadroomTokenBudget,
		CarinaStateDir:      c.StateDir,
	}); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

func envStr(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func envBool(key string, dst *bool) {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			*dst = b
		}
	}
}

func envInt(key string, dst *int) {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			*dst = n
		}
	}
}

func envList(key string, dst *[]string) {
	if v, ok := os.LookupEnv(key); ok {
		var out []string
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		*dst = out
	}
}
