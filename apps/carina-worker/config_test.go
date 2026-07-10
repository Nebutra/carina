package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig([]string{
		"--server", "127.0.0.1:7777",
		"--executor", "/opt/carina/executor",
		"--executor-arg", "--sandbox",
		"--max-concurrency", "2",
		"--lease-ttl", "30s",
		"--renew-interval", "9s",
	})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.MaxConcurrency != 2 || len(cfg.ExecutorArgs) != 1 || cfg.ExecutorArgs[0] != "--sandbox" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestParseConfigRejectsUnsafeTimingAndMissingExecutor(t *testing.T) {
	for _, args := range [][]string{
		{"--server", "127.0.0.1:7777"},
		{"--server", "127.0.0.1:7777", "--executor", "exec", "--max-concurrency", "0"},
		{"--server", "127.0.0.1:7777", "--executor", "exec", "--lease-ttl", "10s", "--renew-interval", "5s"},
		{"--server", "127.0.0.1:7777", "--executor", "exec", "--poll-min-backoff", "2s", "--poll-max-backoff", "1s"},
		{"--server", "daemon.example:7777", "--executor", "exec"},
		{"--gateway", "ws://daemon.example/gateway", "--executor", "exec"},
		{"--server", "127.0.0.1:7777", "--gateway", "wss://daemon.example/gateway", "--executor", "exec"},
	} {
		if _, err := parseConfig(args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestGatewayConfigAndTokenSourcePriority(t *testing.T) {
	t.Setenv("CARINA_GATEWAY_TOKEN", "environment-token")
	tokenFile := filepath.Join(t.TempDir(), "gateway.token")
	if err := os.WriteFile(tokenFile, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig([]string{
		"--gateway", "wss://daemon.example/gateway",
		"--gateway-token-file", tokenFile,
		"--executor", "exec",
	})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	token, err := loadGatewayToken(cfg)
	if err != nil {
		t.Fatalf("loadGatewayToken: %v", err)
	}
	if token != "file-token" {
		t.Fatalf("token = %q, want token file to take precedence", token)
	}
}

func TestGatewayTokenFileMustBePrivate(t *testing.T) {
	t.Setenv("CARINA_GATEWAY_TOKEN", "must-not-fallback")
	tokenFile := filepath.Join(t.TempDir(), "gateway.token")
	if err := os.WriteFile(tokenFile, []byte("token"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testWorkerConfig()
	cfg.Server = ""
	cfg.Gateway = "wss://daemon.example/gateway"
	cfg.GatewayTokenFile = tokenFile
	if _, err := loadGatewayToken(cfg); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("public token file error = %v", err)
	}
}

func TestGatewayTokenEnvironmentFallback(t *testing.T) {
	t.Setenv("CARINA_GATEWAY_TOKEN", "environment-token")
	cfg := testWorkerConfig()
	cfg.Server = ""
	cfg.Gateway = "wss://daemon.example/gateway"
	token, err := loadGatewayToken(cfg)
	if err != nil {
		t.Fatalf("loadGatewayToken: %v", err)
	}
	if token != "environment-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestLoopbackTCPValidation(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7777", "[::1]:7777"} {
		if err := validateLoopbackServer(address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"localhost:7777", "daemon.example:7777", "192.0.2.1:7777", "127.0.0.1"} {
		if err := validateLoopbackServer(address); err == nil {
			t.Fatalf("expected %s to be rejected", address)
		}
	}
}

func TestGatewayURLValidation(t *testing.T) {
	for _, rawURL := range []string{"ws://127.0.0.1:7001/gateway", "ws://[::1]:7001/gateway", "wss://gateway.example/gateway"} {
		if err := validateGatewayURL(rawURL); err != nil {
			t.Fatalf("%s: %v", rawURL, err)
		}
	}
	for _, rawURL := range []string{"ws://localhost:7001/gateway", "ws://gateway.example/gateway", "http://gateway.example", "wss://user@gateway.example/gateway"} {
		if err := validateGatewayURL(rawURL); err == nil {
			t.Fatalf("expected %s to be rejected", rawURL)
		}
	}
}

func TestUsageNamesExecutorWorkspaceBoundary(t *testing.T) {
	if !strings.Contains(usageText, "executor") || !strings.Contains(usageText, "workspace") || !strings.Contains(usageText, executorResultSchema) {
		t.Fatalf("usage does not explain executor contract:\n%s", usageText)
	}
	if strings.Contains(usageText, "--gateway-token ") || !strings.Contains(usageText, "CARINA_GATEWAY_TOKEN") {
		t.Fatalf("usage encourages an unsafe token source:\n%s", usageText)
	}
}
