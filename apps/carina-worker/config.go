package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type workerConfig struct {
	Server            string
	Gateway           string
	GatewayTokenFile  string
	Name              string
	Kind              string
	Executor          string
	ExecutorArgs      stringList
	MaxConcurrency    int
	HeartbeatInterval time.Duration
	LeaseTTL          time.Duration
	RenewInterval     time.Duration
	PollMinBackoff    time.Duration
	PollMaxBackoff    time.Duration
	ExecutorTimeout   time.Duration
	DrainTimeout      time.Duration
}

func defaultWorkerConfig() workerConfig {
	return workerConfig{
		Name:              hostname(),
		Kind:              "remote",
		MaxConcurrency:    1,
		HeartbeatInterval: 10 * time.Second,
		LeaseTTL:          30 * time.Second,
		RenewInterval:     10 * time.Second,
		PollMinBackoff:    250 * time.Millisecond,
		PollMaxBackoff:    5 * time.Second,
		ExecutorTimeout:   30 * time.Minute,
		DrainTimeout:      30 * time.Second,
	}
}

func (c workerConfig) validate() error {
	server := strings.TrimSpace(c.Server)
	gateway := strings.TrimSpace(c.Gateway)
	if (server == "") == (gateway == "") {
		return fmt.Errorf("exactly one of --server or --gateway is required")
	}
	if server != "" {
		if err := validateLoopbackServer(server); err != nil {
			return err
		}
	}
	if gateway != "" {
		if err := validateGatewayURL(gateway); err != nil {
			return err
		}
		if strings.TrimSpace(c.GatewayTokenFile) == "" && strings.TrimSpace(os.Getenv("CARINA_GATEWAY_TOKEN")) == "" {
			return fmt.Errorf("gateway authentication requires --gateway-token-file or CARINA_GATEWAY_TOKEN")
		}
	}
	if strings.TrimSpace(c.Executor) == "" {
		return fmt.Errorf("--executor is required")
	}
	switch c.Kind {
	case "remote", "ci", "sandbox":
	default:
		return fmt.Errorf("--kind must be remote, ci, or sandbox")
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 128 {
		return fmt.Errorf("--max-concurrency must be between 1 and 128")
	}
	if c.HeartbeatInterval <= 0 {
		return fmt.Errorf("--heartbeat must be positive")
	}
	if c.LeaseTTL < time.Second {
		return fmt.Errorf("--lease-ttl must be at least 1s")
	}
	if c.RenewInterval <= 0 || c.RenewInterval >= c.LeaseTTL/2 {
		return fmt.Errorf("--renew-interval must be positive and less than half --lease-ttl")
	}
	if c.PollMinBackoff <= 0 || c.PollMaxBackoff < c.PollMinBackoff {
		return fmt.Errorf("poll backoff must be positive and max must be >= min")
	}
	if c.ExecutorTimeout <= 0 {
		return fmt.Errorf("--executor-timeout must be positive")
	}
	if c.DrainTimeout <= 0 {
		return fmt.Errorf("--drain-timeout must be positive")
	}
	return nil
}

func validateLoopbackServer(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(port) == "" {
		return fmt.Errorf("--server must be a loopback host:port address")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--server only permits loopback addresses; use --gateway wss://... for remote workers")
	}
	return nil
}

func validateGatewayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("--gateway must be a ws:// or wss:// URL")
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("--gateway must use ws:// or wss://")
	}
	if u.User != nil || u.Fragment != "" {
		return fmt.Errorf("--gateway must not contain user info or a fragment")
	}
	if u.Scheme == "ws" && !gatewayHostIsLoopback(u.Hostname()) {
		return fmt.Errorf("remote gateways require wss://; ws:// is loopback-only")
	}
	return nil
}

func gatewayHostIsLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loadGatewayToken(c workerConfig) (string, error) {
	if path := strings.TrimSpace(c.GatewayTokenFile); path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("gateway token file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("gateway token file must be a regular file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("gateway token file must not be accessible by group or other users (use mode 0600)")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("gateway token file: %w", err)
		}
		if token := strings.TrimSpace(string(raw)); token != "" {
			return token, nil
		}
		return "", fmt.Errorf("gateway token file is empty")
	}
	if token := strings.TrimSpace(os.Getenv("CARINA_GATEWAY_TOKEN")); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("gateway token is not configured")
}
