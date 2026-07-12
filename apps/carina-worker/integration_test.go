//go:build integration

// Package main's integration test suite: real carina-daemon and
// carina-worker BINARIES, each its own OS process, talking over a real TCP
// socket — not go/daemon's in-process RPC-handler tests (d.handleWorkPoll
// called directly), and not go/daemon's simulateRemoteWorker helpers (a
// goroutine in the SAME process as the daemon). This is the closest
// achievable proxy for the Agent Swarm design's P4 acceptance criterion
// ("一次 swarm 运行真实跨 2+ 台机器分摊执行") inside a single-machine test
// environment: genuine process isolation and a real network transport
// exercise the exact code paths a second physical machine would, just
// without needing to actually provision one. Gated behind the "integration"
// build tag (not part of `go test ./...`) because it builds and spawns real
// subprocesses — slower and heavier than a unit test, opt-in via
// `make swarm-integration-test`.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

// resolveBinary returns a path to a working binary: the env var if set,
// otherwise `go build`s it fresh into a temp dir. Building on demand means
// this test also runs standalone (`go test -tags integration ./apps/carina-worker/...`)
// without requiring the Makefile's pre-build step, at the cost of a few
// extra seconds the first time.
func resolveBinary(t *testing.T, envVar, pkgPath, outName string) string {
	t.Helper()
	if p := os.Getenv(envVar); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Fatalf("%s=%q does not exist", envVar, p)
	}
	out := filepath.Join(t.TempDir(), outName)
	cmd := exec.Command("go", "build", "-o", out, pkgPath)
	cmd.Dir = repoRootForIntegrationTest(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkgPath, err, output)
	}
	return out
}

func repoRootForIntegrationTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// apps/carina-worker -> repo root is two levels up.
	return filepath.Join(wd, "..", "..")
}

func resolveKernelBinForIntegrationTest(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CARINA_KERNEL_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	root := repoRootForIntegrationTest(t)
	for _, rel := range []string{"target/release/carina-kernel-service", "target/debug/carina-kernel-service"} {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("carina-kernel-service not built (cargo build --release -p carina-kernel --bin carina-kernel-service)")
	return ""
}

func freeIntegrationTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitForUnixSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", path, 50*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon unix socket never appeared: %s", path)
}

func waitForIntegrationTCP(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon tcp listener never appeared: %s", addr)
}

// writeIntegrationExecutor writes a tiny, real (not shell-interpreted —
// carina-worker execs it directly) executor program that drains its stdin
// (the leased task JSON, unused by this smoke test) and emits a valid
// carina.worker.result.v1 result, proving the FULL round trip — a real
// external process actually executed the leased work and reported back —
// rather than a stub inside the test process.
func writeIntegrationExecutor(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "integration-executor.sh")
	script := "#!/bin/sh\ncat >/dev/null\necho '{\"schema_version\":\"carina.worker.result.v1\",\"status\":\"completed\",\"summary\":\"handled by a real separate carina-worker process\",\"patches\":[],\"usage\":{\"input_tokens\":30,\"output_tokens\":12,\"cache_read_tokens\":0,\"cache_write_tokens\":0}}'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func rpcCall(t *testing.T, c *rpc.Client, method string, params any, result any) {
	t.Helper()
	if err := c.Call(method, params, result); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

// TestSwarmRemoteDispatchAcrossRealProcesses is the P4 acceptance test: a
// real carina-daemon process and a real carina-worker process, each its own
// OS process, connected over a real TCP socket (not an in-process function
// call) — the worker registers with a --pool tag, the daemon runs a
// streaming workflow whose one step declares remote+matching affinity, and
// the step only completes because the SEPARATE carina-worker process
// actually leased it, ran a real external executor, and reported back over
// the network.
func TestSwarmRemoteDispatchAcrossRealProcesses(t *testing.T) {
	daemonBin := resolveBinary(t, "CARINA_DAEMON_BIN", "./apps/carina-daemon", "carina-daemon")
	workerBin := resolveBinary(t, "CARINA_WORKER_BIN", "./apps/carina-worker", "carina-worker")
	kernelBin := resolveKernelBinForIntegrationTest(t)
	executorPath := writeIntegrationExecutor(t)

	tmp := t.TempDir()
	// The unix socket path is deliberately NOT under t.TempDir(): macOS
	// bounds sockaddr_un's sun_path to ~104 bytes, and t.TempDir()'s
	// default location (nested under $TMPDIR, which itself is a long
	// per-process /var/folders/... path on macOS, plus the test name and a
	// subtest counter) reliably exceeds that — bind() fails with EINVAL, not
	// a path-not-found error, which is exactly what happened before this
	// fix. A short, explicit /tmp-rooted directory keeps the literal path
	// string short regardless of how long $TMPDIR happens to be; /tmp
	// itself being a symlink to /private/tmp doesn't matter here because
	// bind()/connect() bound the LITERAL string's byte length, not the
	// resolved target's.
	socketDir, err := os.MkdirTemp("/tmp", "carina-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "d.sock")
	tcpAddr := freeIntegrationTCPAddr(t)
	stateDir := filepath.Join(tmp, "state")
	ws := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(filepath.Join(ws, ".carina", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	workflowJSON := `{
		"name": "integration",
		"execution_mode": "streaming",
		"steps": [
			{"id": "offload", "agent": "unused-for-remote-steps", "task": "INTEGRATION_STEP",
			 "remote": true, "affinity": {"worker_pool": "gpu-heavy"}}
		]
	}`
	if err := os.WriteFile(filepath.Join(ws, ".carina", "workflows", "integration.json"), []byte(workflowJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	daemonCmd := exec.Command(daemonBin,
		"-state", stateDir, "-socket", socket, "-tcp", tcpAddr, "-kernel", kernelBin, "-offline")
	daemonCmd.Stdout = os.Stderr // surface daemon logs on test failure, not stdout (keeps go test output parseable)
	daemonCmd.Stderr = os.Stderr
	if err := daemonCmd.Start(); err != nil {
		t.Fatalf("start carina-daemon: %v", err)
	}
	t.Cleanup(func() { _ = daemonCmd.Process.Kill() })
	waitForUnixSocket(t, socket, 15*time.Second)
	waitForIntegrationTCP(t, tcpAddr, 15*time.Second)

	control, err := rpc.Dial(socket)
	if err != nil {
		t.Fatalf("dial daemon control socket: %v", err)
	}
	defer control.Close()

	var sess struct {
		SessionID string `json:"session_id"`
	}
	rpcCall(t, control, "session.create", map[string]any{"workspace_root": ws, "profile": "safe-edit"}, &sess)

	workerCmd := exec.Command(workerBin,
		"-server", tcpAddr, "-name", "integration-worker", "-pool", "gpu-heavy",
		"-executor", executorPath, "-max-concurrency", "1",
		"-poll-min-backoff", "20ms", "-poll-max-backoff", "100ms", "-heartbeat", "200ms")
	workerCmd.Stdout = os.Stderr
	workerCmd.Stderr = os.Stderr
	if err := workerCmd.Start(); err != nil {
		t.Fatalf("start carina-worker: %v", err)
	}
	t.Cleanup(func() { _ = workerCmd.Process.Kill() })

	var run struct {
		ID string `json:"id"`
	}
	rpcCall(t, control, "workflow.run", map[string]any{"session_id": sess.SessionID, "workflow": "integration", "input": ""}, &run)
	if run.ID == "" {
		t.Fatal("workflow.run did not return a run id")
	}

	deadline := time.Now().Add(30 * time.Second)
	var detail map[string]any
	for time.Now().Before(deadline) {
		detail = nil
		rpcCall(t, control, "workflow.detail", map[string]any{"run_id": run.ID}, &detail)
		runInfo, _ := detail["run"].(map[string]any)
		status, _ := runInfo["status"].(string)
		if status == "completed" || status == "failed" || status == "stopped" || status == "interrupted" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if detail == nil {
		t.Fatal("workflow.detail never returned")
	}
	runInfo, _ := detail["run"].(map[string]any)
	status, _ := runInfo["status"].(string)
	if status != "completed" {
		b, _ := json.MarshalIndent(detail, "", "  ")
		t.Fatalf("expected the run to complete via the real separate carina-worker process, got status=%q:\n%s", status, b)
	}
	steps, _ := runInfo["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("expected exactly 1 step in the run detail, got %d", len(steps))
	}
	step, _ := steps[0].(map[string]any)
	output, _ := step["output"].(string)
	if output != "handled by a real separate carina-worker process" {
		t.Fatalf("expected the step's output to be the real external executor's summary, got %q", output)
	}
	if tokens, _ := step["tokens_used"].(float64); tokens != 42 {
		t.Fatalf("expected measured remote usage of 42 tokens, got %#v", step["tokens_used"])
	}
	if observed, _ := step["token_usage_status"].(string); observed != "observed" {
		t.Fatalf("expected remote token usage to be marked observed: %#v", step)
	}
	if unmetered, _ := runInfo["unmetered_steps"].(float64); unmetered != 0 {
		t.Fatalf("expected a complete measured rollup, got unmetered_steps=%v", runInfo["unmetered_steps"])
	}
	fmt.Printf("integration: streaming workflow step completed via a real, separate carina-worker process (run=%s)\n", run.ID)
}
