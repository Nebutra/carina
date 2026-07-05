package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nebutra/carina/go/kernel"
)

// loadOrgPolicy reads the enterprise policy bundle from a config directory
// (PRD §5 Phase 5). All files are optional:
//
//	<dir>/bundle.toml        mandatory-deny policy bundle
//	<dir>/trusted-keys       one base64 ed25519 publisher key per line
//	<dir>/approval.json      [{"min_risk":4,"role":"security-lead"}, ...]
//
// Returns nil when no policy is configured, so open-source/local use pays
// no cost.
func loadOrgPolicy(dir string) *kernel.OrgPolicy {
	if dir == "" {
		return nil
	}
	org := &kernel.OrgPolicy{}
	any := false

	if raw, err := os.ReadFile(filepath.Join(dir, "bundle.toml")); err == nil {
		org.BundleTOML = string(raw)
		any = true
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "trusted-keys")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				org.TrustedPluginKeys = append(org.TrustedPluginKeys, line)
			}
		}
		any = any || len(org.TrustedPluginKeys) > 0
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "approval.json")); err == nil {
		var rules []kernel.ApprovalRule
		if json.Unmarshal(raw, &rules) == nil {
			org.ApprovalPolicy = rules
			any = any || len(rules) > 0
		}
	}
	if !any {
		return nil
	}
	return org
}
