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
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nebutra/carina/go/contextengine"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/nebutra"
)

// ErrInvalidTUILocale lets launchers render a fully localized validation
// message instead of embedding an English parser error in another locale.
var ErrInvalidTUILocale = errors.New("invalid tui_locale")

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
	MemoryProvider             string              `json:"memory_provider"`
	MemoryHMSEndpoint          string              `json:"memory_hms_endpoint"`
	MemoryHMSAPIKeyEnv         string              `json:"memory_hms_api_key_env"`
	MemoryHMSTimeoutMS         int                 `json:"memory_hms_timeout_ms"`
	MemoryHMSMaxEvidence       int                 `json:"memory_hms_max_evidence"`
	MemoryHMSBankKeyEnv        string              `json:"memory_hms_bank_key_env"`
	MemoryHMSProjectionEnabled bool                `json:"memory_hms_projection_enabled"`
	MemoryHMSProjectionPollMS  int                 `json:"memory_hms_projection_poll_ms"`
	TUIKeybindings             map[string][]string `json:"tui_keybindings"`
	TUILocale                  string              `json:"tui_locale"`
	TUIAlternateScreen         string              `json:"tui_alternate_screen"`
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
		MemoryProvider:            "off",
		MemoryHMSTimeoutMS:        3000,
		MemoryHMSMaxEvidence:      8,
		MemoryHMSProjectionPollMS: 1000,
		TUIAlternateScreen:        "auto",
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
		projectPath := filepath.Join(projectDir, ".carina", "config.json")
		if err := rejectProjectMemoryProviderConfig(projectPath); err != nil {
			return cfg, nil, err
		}
		if err := mergeFile(&cfg, projectPath); err != nil {
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
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
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
	envStr("CARINA_TUI_LOCALE", &cfg.TUILocale)
	envStr("CARINA_TUI_ALTERNATE_SCREEN", &cfg.TUIAlternateScreen)
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
	envStr("CARINA_MEMORY_PROVIDER", &cfg.MemoryProvider)
	envStr("CARINA_MEMORY_HMS_ENDPOINT", &cfg.MemoryHMSEndpoint)
	envStr("CARINA_MEMORY_HMS_API_KEY_ENV", &cfg.MemoryHMSAPIKeyEnv)
	envInt("CARINA_MEMORY_HMS_TIMEOUT_MS", &cfg.MemoryHMSTimeoutMS)
	envInt("CARINA_MEMORY_HMS_MAX_EVIDENCE", &cfg.MemoryHMSMaxEvidence)
	envStr("CARINA_MEMORY_HMS_BANK_KEY_ENV", &cfg.MemoryHMSBankKeyEnv)
	envBool("CARINA_MEMORY_HMS_PROJECTION_ENABLED", &cfg.MemoryHMSProjectionEnabled)
	envInt("CARINA_MEMORY_HMS_PROJECTION_POLL_MS", &cfg.MemoryHMSProjectionPollMS)
	envList("CARINA_EGRESS_ALLOW", &cfg.EgressAllow)
}

// Validate rejects nonsensical values (fail fast at startup).
func (c Config) Validate() error {
	provider := strings.ToLower(strings.TrimSpace(c.MemoryProvider))
	switch provider {
	case "", "off":
	case "hms-shadow", "hms-hybrid":
		if err := validateMemoryHMSEndpoint(c.MemoryHMSEndpoint); err != nil {
			return err
		}
		if strings.TrimSpace(c.MemoryHMSBankKeyEnv) == "" {
			return fmt.Errorf("config: memory_hms_bank_key_env is required when memory_provider=%s", provider)
		}
		if strings.TrimSpace(c.MemoryHMSAPIKeyEnv) == "" {
			return fmt.Errorf("config: memory_hms_api_key_env is required when memory_provider=%s", provider)
		}
		if c.MemoryHMSTimeoutMS < 100 || c.MemoryHMSTimeoutMS > 30000 {
			return fmt.Errorf("config: memory_hms_timeout_ms must be between 100 and 30000")
		}
		if c.MemoryHMSMaxEvidence < 1 || c.MemoryHMSMaxEvidence > 50 {
			return fmt.Errorf("config: memory_hms_max_evidence must be between 1 and 50")
		}
		if c.MemoryHMSProjectionPollMS < 100 || c.MemoryHMSProjectionPollMS > 60000 {
			return fmt.Errorf("config: memory_hms_projection_poll_ms must be between 100 and 60000")
		}
	default:
		return fmt.Errorf("config: memory_provider must be one of off, hms-shadow, hms-hybrid")
	}
	if c.MemoryHMSProjectionEnabled && provider != "hms-shadow" && provider != "hms-hybrid" {
		return fmt.Errorf("config: memory_hms_projection_enabled requires an HMS memory provider")
	}
	if strings.TrimSpace(c.TUILocale) != "" {
		if _, err := microcopy.CanonicalLocale(c.TUILocale); err != nil {
			return fmt.Errorf("config: tui_locale: %w", ErrInvalidTUILocale)
		}
	}
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
	if mode := strings.ToLower(strings.TrimSpace(c.TUIAlternateScreen)); mode != "" && mode != "auto" && mode != "always" && mode != "never" {
		return fmt.Errorf("config: tui_alternate_screen must be one of auto, always, never")
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

var projectRestrictedMemoryKeys = map[string]bool{
	"memory_provider": true, "memory_hms_endpoint": true,
	"memory_hms_api_key_env": true, "memory_hms_timeout_ms": true,
	"memory_hms_max_evidence": true, "memory_hms_bank_key_env": true,
	"memory_hms_projection_enabled": true, "memory_hms_projection_poll_ms": true,
}

func rejectProjectMemoryProviderConfig(path string) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	} // mergeFile reports the canonical parse error.
	for key := range values {
		if projectRestrictedMemoryKeys[key] {
			return fmt.Errorf("config: project file %s cannot set deployment-owned key %q", path, key)
		}
	}
	return nil
}

func validateMemoryHMSEndpoint(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("config: memory_hms_endpoint must be an absolute URL without credentials, query, or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return nil
	}
	return fmt.Errorf("config: memory_hms_endpoint must use https (http is allowed only for loopback)")
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
