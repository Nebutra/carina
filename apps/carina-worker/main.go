// carina-worker joins a Carina daemon and executes remotely leased tasks.
// Workspace and sandbox provisioning are delegated to the explicitly configured
// executor; the worker itself only implements the authenticated lease protocol.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Nebutra/carina/go/rpc"
)

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "carina-worker: %v\n\n%s", err, usageText)
		os.Exit(2)
	}

	var client workerRPCClient
	if cfg.Gateway != "" {
		token, tokenErr := loadGatewayToken(cfg)
		if tokenErr != nil {
			log.Fatalf("carina-worker: %v", tokenErr)
		}
		client, err = dialGateway(cfg.Gateway, token)
	} else {
		client, err = rpc.DialTCP(cfg.Server)
	}
	if err != nil {
		log.Fatalf("carina-worker: connect: %v", err)
	}
	defer client.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := newLeaseWorker(client, newCommandExecutor(cfg.Executor, cfg.ExecutorArgs), cfg, log.Default())
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("carina-worker: %v", err)
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "worker"
	}
	return h
}

// A dedicated FlagSet keeps flag parsing testable and avoids flag package
// globals leaking between tests.
func parseConfig(args []string) (workerConfig, error) {
	cfg := defaultWorkerConfig()
	fs := flag.NewFlagSet("carina-worker", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Server, "server", "", "loopback daemon TCP address, e.g. 127.0.0.1:7777")
	fs.StringVar(&cfg.Gateway, "gateway", "", "authenticated ws:// or wss:// Gateway URL")
	fs.StringVar(&cfg.GatewayTokenFile, "gateway-token-file", "", "private file containing the Gateway token")
	fs.StringVar(&cfg.Name, "name", cfg.Name, "worker name")
	fs.StringVar(&cfg.Kind, "kind", cfg.Kind, "worker kind: remote|ci|sandbox")
	fs.DurationVar(&cfg.HeartbeatInterval, "heartbeat", cfg.HeartbeatInterval, "heartbeat interval")
	fs.DurationVar(&cfg.LeaseTTL, "lease-ttl", cfg.LeaseTTL, "lease visibility timeout")
	fs.DurationVar(&cfg.RenewInterval, "renew-interval", cfg.RenewInterval, "lease renewal interval")
	fs.DurationVar(&cfg.PollMinBackoff, "poll-min-backoff", cfg.PollMinBackoff, "minimum empty-queue poll backoff")
	fs.DurationVar(&cfg.PollMaxBackoff, "poll-max-backoff", cfg.PollMaxBackoff, "maximum empty-queue poll backoff")
	fs.DurationVar(&cfg.ExecutorTimeout, "executor-timeout", cfg.ExecutorTimeout, "maximum duration for one executor process")
	fs.DurationVar(&cfg.DrainTimeout, "drain-timeout", cfg.DrainTimeout, "maximum SIGTERM drain duration")
	fs.IntVar(&cfg.MaxConcurrency, "max-concurrency", cfg.MaxConcurrency, "maximum simultaneous leases")
	fs.StringVar(&cfg.Executor, "executor", "", "executor program (required; no shell expansion)")
	fs.Var(&cfg.ExecutorArgs, "executor-arg", "executor argument (repeatable)")
	if err := fs.Parse(args); err != nil {
		return workerConfig{}, err
	}
	if fs.NArg() != 0 {
		return workerConfig{}, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	if err := cfg.validate(); err != nil {
		return workerConfig{}, err
	}
	return cfg, nil
}

const usageText = `usage: carina-worker (--gateway <wss-url> | --server <loopback-host:port>) --executor <program> [options]

The executor receives the leased task JSON on stdin and must emit one JSON
object on stdout using schema_version "carina.worker.result.v1". The executor,
not carina-worker, is responsible for controlled workspace or sandbox setup.

Remote workers should use --gateway with a private --gateway-token-file or the
CARINA_GATEWAY_TOKEN environment variable. --server is loopback-only and is
intended for a local daemon or an operator-authenticated tunnel.
`

type workerRPCClient interface {
	rpcCaller
	Close() error
}
