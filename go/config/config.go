// Package config resolves daemon configuration from a layered cascade, later
// layers overriding earlier ones:
//
//  1. built-in defaults
//  2. global   ~/.carina/config.json
//  3. project  <projectDir>/.carina/config.json
//  4. environment (CARINA_*)
//
// The caller (the daemon entrypoint) applies command-line flags as a final,
// highest-precedence layer on top of the resolved config.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nebutra/carina/go/nebutra"
)

// Config is the resolved daemon configuration. JSON tags name the keys accepted
// in the config files.
type Config struct {
	StateDir              string   `json:"state_dir"`
	Socket                string   `json:"socket"`
	TCP                   string   `json:"tcp"`
	KernelBin             string   `json:"kernel_bin"`
	ToolsDir              string   `json:"tools_dir"`
	PolicyDir             string   `json:"policy_dir"`
	Offline               bool     `json:"offline"`
	MaxConcurrentTasks    int      `json:"max_concurrent_tasks"`
	RequireWorkspaceTrust bool     `json:"require_workspace_trust"`
	MaxTaskTokens         int      `json:"max_task_tokens"`
	EnableEgressProxy     bool     `json:"enable_egress_proxy"`
	EgressAllow           []string `json:"egress_allow"`
	SandboxCommands       bool     `json:"sandbox_commands"`
	InteractiveApproval   bool     `json:"interactive_approval"`
	SummarizerModel       string   `json:"summarizer_model"`
	RiskReviewMode        string   `json:"risk_review_mode"`
	RiskReviewModel       string   `json:"risk_review_model"`
	NebutraCloudEndpoint  string   `json:"nebutra_cloud_endpoint"`
	NebutraSyncMode       string   `json:"nebutra_sync_mode"`
}

// Defaults returns the built-in baseline, anchored at the user's ~/.carina dir.
func Defaults(home string) Config {
	base := filepath.Join(home, ".carina")
	return Config{
		StateDir:             filepath.Join(base, "state"),
		Socket:               filepath.Join(base, "daemon.sock"),
		PolicyDir:            filepath.Join(base, "policy"),
		MaxConcurrentTasks:   8,
		NebutraCloudEndpoint: nebutra.DefaultCloudEndpoint,
		NebutraSyncMode:      nebutra.SyncModeOff,
	}
}

// Load resolves the config cascade. Missing files are skipped; a malformed file
// is a hard error (fail fast rather than silently run mis-configured).
func Load(home, projectDir string) (Config, error) {
	cfg := Defaults(home)
	if err := mergeFile(&cfg, filepath.Join(home, ".carina", "config.json")); err != nil {
		return cfg, err
	}
	if projectDir != "" {
		if err := mergeFile(&cfg, filepath.Join(projectDir, ".carina", "config.json")); err != nil {
			return cfg, err
		}
	}
	mergeEnv(&cfg)
	return cfg, cfg.Validate()
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
	envStr("CARINA_KERNEL_BIN", &cfg.KernelBin)
	envStr("CARINA_TOOLS_DIR", &cfg.ToolsDir)
	envStr("CARINA_POLICY_DIR", &cfg.PolicyDir)
	envStr("CARINA_SUMMARIZER_MODEL", &cfg.SummarizerModel)
	envStr("CARINA_RISK_REVIEW_MODE", &cfg.RiskReviewMode)
	envStr("CARINA_RISK_REVIEW_MODEL", &cfg.RiskReviewModel)
	envStr("CARINA_NEBUTRA_CLOUD_ENDPOINT", &cfg.NebutraCloudEndpoint)
	envStr("CARINA_NEBUTRA_SYNC_MODE", &cfg.NebutraSyncMode)
	envBool("CARINA_OFFLINE", &cfg.Offline)
	envBool("CARINA_REQUIRE_WORKSPACE_TRUST", &cfg.RequireWorkspaceTrust)
	envBool("CARINA_ENABLE_EGRESS_PROXY", &cfg.EnableEgressProxy)
	envBool("CARINA_SANDBOX_COMMANDS", &cfg.SandboxCommands)
	envBool("CARINA_INTERACTIVE_APPROVAL", &cfg.InteractiveApproval)
	envInt("CARINA_MAX_CONCURRENT_TASKS", &cfg.MaxConcurrentTasks)
	envInt("CARINA_MAX_TASK_TOKENS", &cfg.MaxTaskTokens)
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
	if mode := strings.ToLower(strings.TrimSpace(c.RiskReviewMode)); mode != "" && mode != "off" && mode != "advisory" && mode != "enforce" {
		return fmt.Errorf("config: risk_review_mode must be one of off, advisory, enforce")
	}
	if _, err := nebutra.NormalizeCloudEndpoint(c.NebutraCloudEndpoint); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if _, err := nebutra.NormalizeSyncMode(c.NebutraSyncMode); err != nil {
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
