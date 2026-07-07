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
	tcp := flag.String("tcp", cfg.TCP, "optional TCP listen address for remote workers, e.g. :7777")
	gatewayWS := flag.String("gateway-ws", cfg.GatewayWS, "optional WebSocket Gateway listen address, e.g. 127.0.0.1:8777")
	gatewayWSOrigins := flag.String("gateway-ws-origins", strings.Join(cfg.GatewayWSOrigins, ","), "comma-separated allowed browser Origin values for -gateway-ws")
	kernelBin := flag.String("kernel", cfg.KernelBin, "carina-kernel-service path (default: auto-discover)")
	toolsDir := flag.String("tools", cfg.ToolsDir, "zig native tools directory (default: auto-discover)")
	policyDir := flag.String("policy", cfg.PolicyDir, "enterprise org-policy directory")
	offline := flag.Bool("offline", cfg.Offline, "offline mode: disable network model providers")
	maxConcurrent := flag.Int("max-concurrent", cfg.MaxConcurrentTasks, "cap on concurrent background runs")
	maxTokens := flag.Int("max-task-tokens", cfg.MaxTaskTokens, "per-task token budget (0 = unlimited)")
	requireTrust := flag.Bool("require-trust", cfg.RequireWorkspaceTrust, "deny command exec in untrusted workspaces")
	sandbox := flag.Bool("sandbox", cfg.SandboxCommands, "run commands under an OS syscall sandbox")
	egress := flag.Bool("egress", cfg.EnableEgressProxy, "route command network through a deny-by-default egress proxy")
	egressAllow := flag.String("egress-allow", strings.Join(cfg.EgressAllow, ","), "comma-separated hosts allowed when -egress is on")
	interactiveApproval := flag.Bool("interactive-approval", cfg.InteractiveApproval, "pause for an operator decision on requires_approval instead of auto-approving")
	riskReviewMode := flag.String("risk-review-mode", cfg.RiskReviewMode, "autonomous approval risk review mode: off|advisory|enforce")
	riskReviewModel := flag.String("risk-review-model", cfg.RiskReviewModel, "optional model for Nebutra Risk Review (default: local heuristic)")
	nebutraCloud := flag.String("nebutra-cloud", cfg.NebutraCloudEndpoint, "Nebutra Cloud endpoint for identity/sync boundary")
	nebutraSyncMode := flag.String("nebutra-sync-mode", cfg.NebutraSyncMode, "Nebutra sync mode (currently only off)")
	flag.Parse()

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
		StateDir:              *stateDir,
		KernelBin:             *kernelBin,
		ToolsDir:              *toolsDir,
		PolicyDir:             *policyDir,
		Offline:               *offline,
		MaxConcurrentTasks:    *maxConcurrent,
		MaxTaskTokens:         *maxTokens,
		RequireWorkspaceTrust: *requireTrust,
		SandboxCommands:       *sandbox,
		EnableEgressProxy:     *egress,
		EgressAllow:           splitList(*egressAllow),
		InteractiveApproval:   *interactiveApproval,
		RiskReviewMode:        *riskReviewMode,
		RiskReviewModel:       *riskReviewModel,
		NebutraCloudEndpoint:  *nebutraCloud,
		NebutraSyncMode:       *nebutraSyncMode,
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
