package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func TestCmdCostHumanAndJSONPreservesUnknownFields(t *testing.T) {
	s := rpc.NewServer()
	var got map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "usage.cost", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &got); err != nil {
			return nil, err
		}
		return map[string]any{
			"estimated": true,
			"totals": map[string]any{
				"input_tokens": 100, "output_tokens": 20,
				"cache_read_tokens": 30, "cache_write_tokens": 5, "cost_usd": 0.0123,
			},
			"providers": []map[string]any{{
				"provider": "openai", "model": "gpt-5",
				"input_tokens": 100, "output_tokens": 20,
				"cache_read_tokens": 30, "cache_write_tokens": 5, "cost_usd": 0.0123,
				"pricing_known": true, "future_field": "kept",
			}},
			"future_top_level": map[string]any{"version": 2},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := dialTestServer(t, s)
	defer c.Close()

	human, err := captureStdout(t, func() error { return cmdCost(c, []string{"sess_1"}) })
	if err != nil {
		t.Fatal(err)
	}
	if got["session_id"] != "sess_1" {
		t.Fatalf("usage.cost params = %#v", got)
	}
	for _, want := range []string{"estimated: true", "cost_usd=0.012300", "openai/gpt-5", "pricing=known"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human cost output missing %q:\n%s", want, human)
		}
	}

	jsonOutput, err := captureStdout(t, func() error { return cmdCost(c, []string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"future_top_level"`, `"future_field": "kept"`} {
		if !strings.Contains(jsonOutput, want) {
			t.Fatalf("JSON cost output dropped unknown field %q:\n%s", want, jsonOutput)
		}
	}
	if err := cmdCost(c, []string{"sess_1", "sess_2"}); err == nil {
		t.Fatal("multiple session ids should fail")
	}
}

func TestCmdForkCallsSessionFork(t *testing.T) {
	s := rpc.NewServer()
	var got map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "session.fork", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &got); err != nil {
			return nil, err
		}
		return map[string]any{"session_id": "sess_child", "parent_id": got["session_id"]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := dialTestServer(t, s)
	defer c.Close()

	out, err := captureStdout(t, func() error { return cmdFork(c, []string{"sess_parent"}) })
	if err != nil {
		t.Fatal(err)
	}
	if got["session_id"] != "sess_parent" || !strings.Contains(out, `"session_id": "sess_child"`) {
		t.Fatalf("unexpected fork params/output: %#v\n%s", got, out)
	}
	if err := cmdFork(c, nil); err == nil {
		t.Fatal("missing session id should fail")
	}
}

func TestCmdWorkerUsesOnlyRegisteredLifecycleRPCs(t *testing.T) {
	s := rpc.NewServer()
	calls := map[string]map[string]any{}
	register := func(method string, result any) {
		t.Helper()
		if err := s.RegisterMethod(rpc.MethodDescriptor{Method: method, Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
			var decoded map[string]any
			if err := json.Unmarshal(params, &decoded); err != nil {
				return nil, err
			}
			calls[method] = decoded
			return result, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	register("worker.list", []map[string]any{{"worker_id": "wrk_1", "status": "idle"}})
	register("worker.register", map[string]any{"worker_id": "wrk_2", "worker_credential": "cred_2"})
	register("worker.heartbeat", map[string]any{"ok": true})
	register("worker.revoke", map[string]any{"ok": true})
	c := dialTestServer(t, s)
	defer c.Close()

	for _, args := range [][]string{{"list"}, {"register", "build-1", "ci"}, {"heartbeat", "wrk_2", "cred_2"}, {"revoke", "wrk_2", "cred_2"}} {
		if _, err := captureStdout(t, func() error { return cmdWorker(c, args) }); err != nil {
			t.Fatalf("cmdWorker(%v): %v", args, err)
		}
	}
	if calls["worker.register"]["name"] != "build-1" || calls["worker.register"]["kind"] != "ci" {
		t.Fatalf("worker.register params = %#v", calls["worker.register"])
	}
	for _, method := range []string{"worker.heartbeat", "worker.revoke"} {
		if calls[method]["worker_id"] != "wrk_2" || calls[method]["worker_credential"] != "cred_2" {
			t.Fatalf("%s params = %#v", method, calls[method])
		}
	}
	if err := cmdWorker(c, []string{"register", "build-2", "local"}); err == nil {
		t.Fatal("CLI must not expose local worker registration")
	}
}

func TestStopOwnedDaemonRequiresLivePIDMatch(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "daemon.sock")
	record := daemonOwnershipRecord{Owner: daemonOwnershipMarker, PID: 43210, Socket: socket, StartedAt: time.Now().UTC()}
	raw, _ := json.Marshal(record)
	if err := writePrivateFileAtomic(daemonOwnershipPath(socket), raw); err != nil {
		t.Fatal(err)
	}

	origStatus := daemonStatusHook
	origSignal := signalDaemonHook
	defer func() { daemonStatusHook, signalDaemonHook = origStatus, origSignal }()

	signaled := false
	daemonStatusHook = func(gotSocket string) (daemonStatus, error) {
		if gotSocket != socket {
			t.Fatalf("status socket = %q", gotSocket)
		}
		return daemonStatus{PID: record.PID + 1}, nil
	}
	signalDaemonHook = func(int, os.Signal) error { signaled = true; return nil }
	if err := stopOwnedDaemon(socket); err == nil || !strings.Contains(err.Error(), "live daemon reports pid") {
		t.Fatalf("PID mismatch error = %v", err)
	}
	if signaled {
		t.Fatal("PID mismatch must not signal a process")
	}

	daemonStatusHook = func(string) (daemonStatus, error) { return daemonStatus{PID: record.PID}, nil }
	signalDaemonHook = func(pid int, signal os.Signal) error {
		if pid != record.PID || signal != syscall.SIGTERM {
			t.Fatalf("signal pid=%d signal=%v", pid, signal)
		}
		signaled = true
		return nil
	}
	if _, err := captureStdout(t, func() error { return stopOwnedDaemon(socket) }); err != nil {
		t.Fatal(err)
	}
	if !signaled {
		t.Fatal("matching owned daemon was not signaled")
	}
}

func TestStopOwnedDaemonRefusesMissingUnsafeOrUnreachable(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	if err := stopOwnedDaemon(socket); err == nil || !strings.Contains(err.Error(), "manually started") {
		t.Fatalf("missing ownership error = %v", err)
	}

	record := daemonOwnershipRecord{Owner: daemonOwnershipMarker, PID: 100, Socket: socket}
	raw, _ := json.Marshal(record)
	path := daemonOwnershipPath(socket)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stopOwnedDaemon(socket); err == nil || !strings.Contains(err.Error(), "group/world accessible") {
		t.Fatalf("unsafe ownership error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	origStatus := daemonStatusHook
	origSignal := signalDaemonHook
	defer func() { daemonStatusHook, signalDaemonHook = origStatus, origSignal }()
	daemonStatusHook = func(string) (daemonStatus, error) { return daemonStatus{}, errors.New("offline") }
	signalDaemonHook = func(int, os.Signal) error {
		t.Fatal("unreachable endpoint must not signal")
		return nil
	}
	if err := stopOwnedDaemon(socket); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("unreachable endpoint error = %v", err)
	}
}

func TestDaemonLogsAndCompletionScripts(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	if err := os.WriteFile(daemonLogPath(socket), []byte("started\nready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	if err := printDaemonLogs(socket, &logs); err != nil {
		t.Fatal(err)
	}
	if logs.String() != "started\nready\n" {
		t.Fatalf("logs = %q", logs.String())
	}

	for _, shell := range []string{"bash", "zsh", "fish"} {
		script, err := completionScript(shell)
		if err != nil {
			t.Fatalf("completionScript(%s): %v", shell, err)
		}
		for _, want := range []string{"carina", "daemon", "worker", "cost", "fork", "update"} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s completion missing %q:\n%s", shell, want, script)
			}
		}
	}
	if _, err := completionScript("powershell"); err == nil {
		t.Fatal("unsupported shell should fail")
	}
}

func dialTestServer(t *testing.T, s *rpc.Server) *rpcClient {
	t.Helper()
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenTCP(addr) }()
	t.Cleanup(func() { _ = s.Close() })
	waitTCP(t, addr)
	c, err := rpc.DialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
