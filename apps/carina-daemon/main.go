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
	"strings"
	"syscall"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/rpc"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	// Resolve the layered config (defaults → global → project → env). Flags,
	// parsed below, are the final highest-precedence layer via their defaults.
	cwd, _ := os.Getwd()
	cfg, err := config.Load(home, cwd)
	if err != nil {
		log.Fatalf("carina-daemon: %v", err)
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
	interactiveApproval := flag.Bool("interactive-approval", cfg.InteractiveApproval, "pause for an operator decision on requires_approval instead of auto-approving")
	enableDebugRPC := flag.Bool("debug-rpc", cfg.EnableDebugRPC, "enable local-only debug.* RPC inspection endpoints and in-memory trace collection")
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
	flag.Parse()
	if err := validateListenerSecurity(*tcp, *gatewayWS, *gatewayTokenSigningKeyFile); err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	// Record which flags the operator set explicitly, so they stay the highest-
	// precedence layer across SIGHUP reloads (a reload must not clobber them).
	pinned := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { pinned[f.Name] = true })

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
		SafeMode:                   *safeMode,
		MaxConcurrentTasks:         *maxConcurrent,
		MaxTaskTokens:              *maxTokens,
		RequireWorkspaceTrust:      *requireTrust,
		SandboxCommands:            *sandbox,
		EnableEgressProxy:          *egress,
		EgressAllow:                splitList(*egressAllow),
		InteractiveApproval:        *interactiveApproval,
		EnableDebugRPC:             *enableDebugRPC,
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
	})
	if err != nil {
		log.Fatalf("carina-daemon: %v", err)
	}

	// Config hot-reload: re-run the cascade and live-apply the reloadable subset,
	// re-pinning any operator-set CLI flags so they remain highest-precedence.
	reload := func() error {
		nc, err := config.Load(home, cwd)
		if err != nil {
			return err
		}
		if pinned["max-task-tokens"] {
			nc.MaxTaskTokens = *maxTokens
		}
		if pinned["interactive-approval"] {
			nc.InteractiveApproval = *interactiveApproval
		}
		if pinned["debug-rpc"] {
			nc.EnableDebugRPC = *enableDebugRPC
		}
		if pinned["risk-review-mode"] {
			nc.RiskReviewMode = *riskReviewMode
		}
		if pinned["require-trust"] {
			nc.RequireWorkspaceTrust = *requireTrust
		}
		if pinned["sandbox"] {
			nc.SandboxCommands = *sandbox
		}
		if pinned["egress-allow"] {
			nc.EgressAllow = splitList(*egressAllow)
		}
		if pinned["context-engine"] {
			nc.ContextEngine = *contextEngine
		}
		if pinned["headroom-bin"] {
			nc.HeadroomBin = *headroomBin
		}
		if pinned["headroom-state-dir"] {
			nc.HeadroomStateDir = *headroomStateDir
		}
		if pinned["headroom-mode"] {
			nc.HeadroomMode = *headroomMode
		}
		if pinned["headroom-proxy-port"] {
			nc.HeadroomProxyPort = *headroomProxyPort
		}
		if pinned["headroom-token-budget"] {
			nc.HeadroomTokenBudget = *headroomTokenBudget
		}
		return d.ApplyConfig(nc)
	}
	d.SetReloader(reload)

	// Auto-reload: watch the config files and reload on change (complements
	// SIGHUP for environments that can't signal the daemon).
	watcher := config.NewWatcher(config.WatchPaths(home, cwd), 0, func() { // 0 => default 3s
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
