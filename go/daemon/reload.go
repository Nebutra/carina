package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/contextengine"
	"github.com/Nebutra/carina/go/egress"
)

// ApplyConfig live-applies the hot-reloadable subset of a config to a running
// daemon (no restart): the per-task token budget, interactive-approval mode,
// debug RPC/trace, risk-review mode, workspace-trust gate, command sandbox,
// best_of_n opt-in, and egress allowlist. It validates first and returns
// WITHOUT mutating on failure, so a bad reload keeps the last-good config.
//
// Restart-only knobs — listeners (unix/TCP/WebSocket Gateway), kernel/tools/
// policy wiring, the concurrency cap (d.runSem is sized once), offline/provider
// setup, the model-backed risk reviewer, and turning the egress proxy itself
// on/off — are intentionally NOT touched here.
func (d *Daemon) ApplyConfig(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := d.setRiskReviewMode(cfg.RiskReviewMode); err != nil {
		return err
	}
	contextEng, err := contextengine.New(contextengine.Config{
		ContextEngine:       cfg.ContextEngine,
		HeadroomBin:         cfg.HeadroomBin,
		HeadroomStateDir:    cfg.HeadroomStateDir,
		HeadroomMode:        cfg.HeadroomMode,
		HeadroomProxyPort:   cfg.HeadroomProxyPort,
		HeadroomTokenBudget: cfg.HeadroomTokenBudget,
		CarinaStateDir:      d.stateDir,
	})
	if err != nil {
		return err
	}
	managedEnabled, err := d.connectContextEngineMCP(contextEng)
	if err != nil {
		return err
	}
	oldContextEng := d.contextEng
	d.contextEng = contextEng
	if d.mcp != nil && (!managedEnabled || !contextEng.Status().ManagedMCPConnected) {
		d.mcp.Disconnect(contextengine.ManagedMCPServerName)
	}
	if oldContextEng != nil {
		_ = oldContextEng.Close()
	}
	d.maxTaskTokens.Store(int64(cfg.MaxTaskTokens))
	d.disableAlwaysApprove.Store(cfg.DisableAlwaysApprove)
	mode := strings.TrimSpace(cfg.ApprovalMode)
	if mode == "" {
		mode = approvalModeFromInteractive(cfg.InteractiveApproval)
	}
	if err := d.setApprovalMode(mode); err != nil {
		// Keep last-good mode if always-approve is newly locked; still apply the lock.
		if d.approvalModeString() == approvalModeAlwaysApprove {
			_ = d.setApprovalMode(approvalModeAsk)
		}
	}
	d.debugRPCEnabled.Store(cfg.EnableDebugRPC)
	d.requireTrust.Store(cfg.RequireWorkspaceTrust)
	d.sandbox.Store(cfg.SandboxCommands)
	d.bestOfNEnabled.Store(cfg.BestOfNEnabled)
	if d.egress != nil {
		d.egress.SetGate(egress.Allowlist(cfg.EgressAllow))
	}
	return nil
}

// SetReloader installs the config-reload closure invoked on SIGHUP and by the
// daemon.reload RPC.
func (d *Daemon) SetReloader(fn func() error) { d.reload = fn }

// handleReload triggers a config reload. Local-only (never on the remote
// allowlist): a remote caller must not be able to reload trust/egress config.
func (d *Daemon) handleReload(_ json.RawMessage) (any, error) {
	if d.reload == nil {
		return nil, fmt.Errorf("no reloader configured")
	}
	if err := d.reload(); err != nil {
		return nil, err
	}
	return map[string]any{"reloaded": true}, nil
}
