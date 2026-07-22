// carina-daemon is the Carina control-plane entrypoint (PRD §7.2). It hosts the
// Rust capability kernel as a child process and serves JSON-RPC on a unix
// socket (and optionally TCP for remote workers).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/rpc"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	// Resolve the layered config (defaults → managed → global → project → env,
	// with managed-locked keys re-applied last). Flags, parsed below, are the
	// final highest-precedence layer via their defaults — except keys locked by
	// the managed file, where an explicitly-set flag is a startup error.
	cwd, _ := os.Getwd()
	managedPath := config.DefaultManagedPath()
	cfg, locks, err := config.LoadWithManaged(home, cwd, managedPath)
	if err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}
	if locks != nil && len(locks.Keys) > 0 {
		log.Printf("carina-daemon: managed config %s locks keys: %s", locks.Source, strings.Join(locks.Keys, ", "))
	}

	stateDir := flag.String("state", cfg.StateDir, "session/event storage directory")
	socket := flag.String("socket", cfg.Socket, "unix socket path")
	tcp := flag.String("tcp", cfg.TCP, "optional loopback-only diagnostic TCP address, e.g. 127.0.0.1:7777")
	gatewayHTTP := flag.String("gateway-http", cfg.GatewayHTTP, "optional HTTP Gateway listen address, e.g. 127.0.0.1:8787")
	gatewayHTTPOrigins := flag.String("gateway-http-origins", strings.Join(cfg.GatewayHTTPOrigins, ","), "comma-separated allowed browser Origin values for -gateway-http")
	gatewayWS := flag.String("gateway-ws", cfg.GatewayWS, "optional WebSocket Gateway listen address, e.g. 127.0.0.1:8777")
	gatewayWSOrigins := flag.String("gateway-ws-origins", strings.Join(cfg.GatewayWSOrigins, ","), "comma-separated allowed browser Origin values for -gateway-ws")
	gatewayTokenSigningKeyFile := flag.String("gateway-token-signing-key-file", cfg.GatewayTokenSigningKeyFile, "optional 0600 file containing Gateway token signing material")
	gatewayTokenMaxTTL := flag.Int("gateway-token-max-ttl", cfg.GatewayTokenMaxTTLSeconds, "max scoped Gateway token TTL in seconds")
	kernelBin := flag.String("kernel", cfg.KernelBin, "carina-kernel-service path (default: auto-discover)")
	toolsDir := flag.String("tools", cfg.ToolsDir, "zig native tools directory (default: auto-discover)")
	policyDir := flag.String("policy", cfg.PolicyDir, "enterprise org-policy directory")
	offline := flag.Bool("offline", cfg.Offline, "offline mode: disable network model providers")
	safeMode := flag.Bool("safe-mode", false, "disable hooks, plugins, MCP, and project commands/agents")
	maxConcurrent := flag.Int("max-concurrent", cfg.MaxConcurrentTasks, "cap on concurrent background runs")
	maxTokens := flag.Int("max-task-tokens", cfg.MaxTaskTokens, "per-task token budget (0 = unlimited)")
	requireTrust := flag.Bool("require-trust", cfg.RequireWorkspaceTrust, "deny command exec in untrusted workspaces")
	sandbox := flag.Bool("sandbox", cfg.SandboxCommands, "run commands under an OS syscall sandbox")
	egress := flag.Bool("egress", cfg.EnableEgressProxy, "route command network through a deny-by-default egress proxy")
	egressAllow := flag.String("egress-allow", strings.Join(cfg.EgressAllow, ","), "comma-separated hosts allowed when -egress is on")
	interactiveApproval := flag.Bool("interactive-approval", cfg.InteractiveApproval, "legacy: true=ask, false=always-approve (prefer -approval-mode)")
	approvalMode := flag.String("approval-mode", cfg.ApprovalMode, "product HITL mode: ask|always-approve|dont-ask (empty uses -interactive-approval)")
	disableAlwaysApprove := flag.Bool("disable-always-approve", cfg.DisableAlwaysApprove, "org lock: refuse always-approve mode")
	enableDebugRPC := flag.Bool("debug-rpc", cfg.EnableDebugRPC, "enable local-only debug.* RPC inspection endpoints and in-memory trace collection")
	bestOfNEnabled := flag.Bool("best-of-n", cfg.BestOfNEnabled, "opt-in: expose the best_of_n tool (experimental; N parallel candidate generations cost roughly Nx a single patch)")
	riskReviewMode := flag.String("risk-review-mode", cfg.RiskReviewMode, "autonomous approval risk review mode: off|advisory|enforce")
	riskReviewModel := flag.String("risk-review-model", cfg.RiskReviewModel, "optional model for Nebutra Risk Review (default: local heuristic)")
	nebutraCloud := flag.String("nebutra-cloud", cfg.NebutraCloudEndpoint, "Nebutra Cloud endpoint for identity/sync boundary")
	nebutraSyncMode := flag.String("nebutra-sync-mode", cfg.NebutraSyncMode, "Nebutra sync mode (currently only off)")
	contextEngine := flag.String("context-engine", cfg.ContextEngine, "context engine: auto|off|headroom|noop")
	headroomBin := flag.String("headroom-bin", cfg.HeadroomBin, "optional bundled/override Headroom binary path")
	headroomStateDir := flag.String("headroom-state-dir", cfg.HeadroomStateDir, "Headroom local state directory")
	headroomMode := flag.String("headroom-mode", cfg.HeadroomMode, "Headroom integration mode: managed_mcp|sidecar|proxy")
	headroomProxyPort := flag.Int("headroom-proxy-port", cfg.HeadroomProxyPort, "Headroom localhost proxy port (0 = choose later)")
	headroomTokenBudget := flag.Int("headroom-token-budget", cfg.HeadroomTokenBudget, "Headroom context token budget")
	memoryProvider := flag.String("memory-provider", cfg.MemoryProvider, "memory recall provider: off|hms-shadow|hms-hybrid")
	memoryHMSEndpoint := flag.String("memory-hms-endpoint", cfg.MemoryHMSEndpoint, "HMS base URL (HTTPS or loopback HTTP)")
	memoryHMSAPIKeyEnv := flag.String("memory-hms-api-key-env", cfg.MemoryHMSAPIKeyEnv, "daemon environment variable containing the HMS bearer token")
	memoryHMSTimeoutMS := flag.Int("memory-hms-timeout-ms", cfg.MemoryHMSTimeoutMS, "HMS recall timeout in milliseconds")
	memoryHMSMaxEvidence := flag.Int("memory-hms-max-evidence", cfg.MemoryHMSMaxEvidence, "maximum HMS evidence rows frozen into a task")
	memoryHMSBankKeyEnv := flag.String("memory-hms-bank-key-env", cfg.MemoryHMSBankKeyEnv, "daemon environment variable containing the HMS bank derivation key")
	memoryHMSProjectionEnabled := flag.Bool("memory-hms-projection", cfg.MemoryHMSProjectionEnabled, "project approved local memory state into HMS")
	memoryHMSProjectionPollMS := flag.Int("memory-hms-projection-poll-ms", cfg.MemoryHMSProjectionPollMS, "HMS projection worker poll interval in milliseconds")
	flag.Parse()
	if err := validateListenerSecurity(*tcp, *gatewayWS, *gatewayTokenSigningKeyFile); err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	// Record which flags the operator set explicitly, so they stay the highest-
	// precedence layer across SIGHUP reloads (a reload must not clobber them).
	pinned := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { pinned[f.Name] = true })

	// Fail closed with provenance when an explicitly-set flag would override a
	// managed-locked key: better an abort naming the lock's source than a
	// silently ignored org policy or a silently ignored operator intent.
	if err := validateLockedFlags(pinned, locks); err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	// Bridge the resolved summarizer model into the env the daemon reads, unless
	// the operator already set it explicitly.
	if cfg.SummarizerModel != "" {
		if _, set := os.LookupEnv("CARINA_SUMMARIZER_MODEL"); !set {
			_ = os.Setenv("CARINA_SUMMARIZER_MODEL", cfg.SummarizerModel)
		}
	}

	if err := os.MkdirAll(filepath.Dir(*socket), 0o700); err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	d, err := daemon.New(daemon.Options{
		StateDir:                   *stateDir,
		KernelBin:                  *kernelBin,
		ToolsDir:                   *toolsDir,
		PolicyDir:                  *policyDir,
		Offline:                    *offline,
		DisabledProviders:          cfg.DisabledProviders,
		SafeMode:                   *safeMode,
		MaxConcurrentTasks:         *maxConcurrent,
		MaxTaskTokens:              *maxTokens,
		RequireWorkspaceTrust:      *requireTrust,
		SandboxCommands:            *sandbox,
		EnableEgressProxy:          *egress,
		EgressAllow:                splitList(*egressAllow),
		InteractiveApproval:        *interactiveApproval,
		ApprovalMode:               *approvalMode,
		DisableAlwaysApprove:       *disableAlwaysApprove,
		EnableDebugRPC:             *enableDebugRPC,
		BestOfNEnabled:             *bestOfNEnabled,
		RiskReviewMode:             *riskReviewMode,
		RiskReviewModel:            *riskReviewModel,
		NebutraCloudEndpoint:       *nebutraCloud,
		NebutraSyncMode:            *nebutraSyncMode,
		GatewayTokenSigningKeyFile: *gatewayTokenSigningKeyFile,
		GatewayTokenMaxTTLSeconds:  *gatewayTokenMaxTTL,
		ContextEngine:              *contextEngine,
		HeadroomBin:                *headroomBin,
		HeadroomStateDir:           *headroomStateDir,
		HeadroomMode:               *headroomMode,
		HeadroomProxyPort:          *headroomProxyPort,
		HeadroomTokenBudget:        *headroomTokenBudget,
		MemoryProvider:             *memoryProvider,
		MemoryHMSEndpoint:          *memoryHMSEndpoint,
		MemoryHMSAPIKeyEnv:         *memoryHMSAPIKeyEnv,
		MemoryHMSTimeout:           time.Duration(*memoryHMSTimeoutMS) * time.Millisecond,
		MemoryHMSMaxEvidence:       *memoryHMSMaxEvidence,
		MemoryHMSBankKeyEnv:        *memoryHMSBankKeyEnv,
		MemoryHMSProjectionEnabled: *memoryHMSProjectionEnabled,
		MemoryHMSProjectionPoll:    time.Duration(*memoryHMSProjectionPollMS) * time.Millisecond,
	})
	if err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	// Config hot-reload: re-run the cascade and live-apply the reloadable subset,
	// re-pinning any operator-set CLI flags so they remain highest-precedence —
	// unless the (freshly re-read) managed file locks the key, in which case the
	// lock re-applied inside LoadWithManaged wins across SIGHUP/auto-reload.
	reload := func() error {
		nc, nlocks, err := config.LoadWithManaged(home, cwd, managedPath)
		if err != nil {
			return err
		}
		repin := func(flagName string) bool {
			return pinned[flagName] && !nlocks.Locked(flagConfigKeys[flagName])
		}
		if repin("max-task-tokens") {
			nc.MaxTaskTokens = *maxTokens
		}
		if repin("interactive-approval") {
			nc.InteractiveApproval = *interactiveApproval
		}
		if repin("approval-mode") {
			nc.ApprovalMode = *approvalMode
		}
		if repin("disable-always-approve") {
			nc.DisableAlwaysApprove = *disableAlwaysApprove
		}
		if repin("debug-rpc") {
			nc.EnableDebugRPC = *enableDebugRPC
		}
		if repin("risk-review-mode") {
			nc.RiskReviewMode = *riskReviewMode
		}
		if repin("require-trust") {
			nc.RequireWorkspaceTrust = *requireTrust
		}
		if repin("sandbox") {
			nc.SandboxCommands = *sandbox
		}
		if repin("egress-allow") {
			nc.EgressAllow = splitList(*egressAllow)
		}
		if repin("context-engine") {
			nc.ContextEngine = *contextEngine
		}
		if repin("headroom-bin") {
			nc.HeadroomBin = *headroomBin
		}
		if repin("headroom-state-dir") {
			nc.HeadroomStateDir = *headroomStateDir
		}
		if repin("headroom-mode") {
			nc.HeadroomMode = *headroomMode
		}
		if repin("headroom-proxy-port") {
			nc.HeadroomProxyPort = *headroomProxyPort
		}
		if repin("headroom-token-budget") {
			nc.HeadroomTokenBudget = *headroomTokenBudget
		}
		return d.ApplyConfig(nc)
	}
	d.SetReloader(reload)

	// Auto-reload: watch the config files and reload on change (complements
	// SIGHUP for environments that can't signal the daemon).
	watcher := config.NewWatcher(config.WatchPaths(home, cwd, managedPath), 0, func() { // 0 => default 3s
		if err := reload(); err != nil {
			log.Printf("carina-daemon: auto-reload failed (keeping last-good): %v", err)
		} else {
			fmt.Println("carina-daemon: config auto-reloaded (file change)")
		}
	})
	go watcher.Run()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigs {
			if sig == syscall.SIGHUP {
				if err := reload(); err != nil {
					log.Printf("carina-daemon: config reload failed (keeping last-good): %v", err)
				} else {
					fmt.Println("carina-daemon: config reloaded (SIGHUP)")
				}
				continue
			}
			fmt.Println("\ncarina-daemon: shutting down")
			_ = d.Close()
			_ = os.Remove(*socket)
			os.Exit(0)
		}
	}()

	if *tcp != "" {
		go func() {
			fmt.Printf("carina-daemon: also listening on tcp %s\n", *tcp)
			if err := d.RunTCP(*tcp); err != nil {
				log.Printf("carina-daemon: tcp: %v", err)
			}
		}()
	}
	if *gatewayHTTP != "" {
		go func() {
			fmt.Printf("carina-daemon: gateway http listening on http://%s\n", *gatewayHTTP)
			if err := d.RunGatewayHTTP(*gatewayHTTP, splitList(*gatewayHTTPOrigins)); err != nil {
				log.Printf("carina-daemon: gateway http: %v", err)
			}
		}()
	}
	if *gatewayWS != "" {
		go func() {
			fmt.Printf("carina-daemon: gateway websocket listening on ws://%s/gateway\n", *gatewayWS)
			if err := d.RunGatewayWebSocket(*gatewayWS, splitList(*gatewayWSOrigins)); err != nil {
				log.Printf("carina-daemon: gateway websocket: %v", err)
			}
		}()
	}

	fmt.Printf("carina-daemon %s listening on %s\n", daemon.Version, *socket)
	if err := d.Run(*socket); err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}
}

// flagConfigKeys maps each CLI flag to the config-file key it overrides
// (flags with no config counterpart, e.g. -safe-mode, are absent).
var flagConfigKeys = map[string]string{
	"state":                          "state_dir",
	"socket":                         "socket",
	"tcp":                            "tcp",
	"gateway-http":                   "gateway_http",
	"gateway-http-origins":           "gateway_http_origins",
	"gateway-ws":                     "gateway_ws",
	"gateway-ws-origins":             "gateway_ws_origins",
	"gateway-token-signing-key-file": "gateway_token_signing_key_file",
	"gateway-token-max-ttl":          "gateway_token_max_ttl_seconds",
	"kernel":                         "kernel_bin",
	"tools":                          "tools_dir",
	"policy":                         "policy_dir",
	"offline":                        "offline",
	"max-concurrent":                 "max_concurrent_tasks",
	"max-task-tokens":                "max_task_tokens",
	"require-trust":                  "require_workspace_trust",
	"sandbox":                        "sandbox_commands",
	"egress":                         "enable_egress_proxy",
	"egress-allow":                   "egress_allow",
	"interactive-approval":           "interactive_approval",
	"approval-mode":                  "approval_mode",
	"disable-always-approve":         "disable_always_approve",
	"debug-rpc":                      "enable_debug_rpc",
	"risk-review-mode":               "risk_review_mode",
	"risk-review-model":              "risk_review_model",
	"nebutra-cloud":                  "nebutra_cloud_endpoint",
	"nebutra-sync-mode":              "nebutra_sync_mode",
	"context-engine":                 "context_engine",
	"headroom-bin":                   "headroom_bin",
	"headroom-state-dir":             "headroom_state_dir",
	"headroom-mode":                  "headroom_mode",
	"headroom-proxy-port":            "headroom_proxy_port",
	"headroom-token-budget":          "headroom_token_budget",
	"memory-provider":                "memory_provider",
	"memory-hms-endpoint":            "memory_hms_endpoint",
	"memory-hms-api-key-env":         "memory_hms_api_key_env",
	"memory-hms-timeout-ms":          "memory_hms_timeout_ms",
	"memory-hms-max-evidence":        "memory_hms_max_evidence",
	"memory-hms-bank-key-env":        "memory_hms_bank_key_env",
	"memory-hms-projection":          "memory_hms_projection_enabled",
	"memory-hms-projection-poll-ms":  "memory_hms_projection_poll_ms",
}

// validateLockedFlags fails closed when an explicitly-set CLI flag collides
// with a managed-locked config key, naming both the key and the managed file
// so the operator sees exactly which policy blocked the override.
func validateLockedFlags(pinned map[string]bool, locks *config.LockReport) error {
	if locks == nil {
		return nil
	}
	names := make([]string, 0, len(pinned))
	for name := range pinned {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		key, ok := flagConfigKeys[name]
		if !ok {
			continue
		}
		if locks.Locked(key) {
			return fmt.Errorf("flag -%s overrides config key %q locked by managed config %s", name, key, locks.Source)
		}
	}
	return nil
}

func validateListenerSecurity(tcpAddr, gatewayWSAddr, gatewayTokenSigningKeyFile string) error {
	if strings.TrimSpace(tcpAddr) != "" {
		if err := rpc.ValidateLoopbackTCPAddress(tcpAddr); err != nil {
			return err
		}
	}
	if strings.TrimSpace(gatewayWSAddr) != "" && strings.TrimSpace(gatewayTokenSigningKeyFile) == "" {
		return fmt.Errorf("gateway websocket requires -gateway-token-signing-key-file")
	}
	return nil
}

// splitList parses a comma-separated flag value into a trimmed, non-empty slice.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
