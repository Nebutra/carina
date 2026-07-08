package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func TestParseRunArgsModel(t *testing.T) {
	prompt, model, agent, err := parseRunArgs([]string{"--model", "openrouter/anthropic/claude-sonnet", "fix tests"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "fix tests" || model != "openrouter/anthropic/claude-sonnet" || agent != "" {
		t.Fatalf("prompt=%q model=%q agent=%q", prompt, model, agent)
	}
}

func TestParseRunArgsShortModel(t *testing.T) {
	prompt, model, agent, err := parseRunArgs([]string{"-m", "openai/gpt-5", "-a", "plan", "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "ship it" || model != "openai/gpt-5" || agent != "plan" {
		t.Fatalf("prompt=%q model=%q agent=%q", prompt, model, agent)
	}
}

func TestParseRunArgsRequiresPrompt(t *testing.T) {
	if _, _, _, err := parseRunArgs([]string{"--model", "openai/gpt-5"}); err == nil {
		t.Fatal("missing prompt should error")
	}
}

func TestUsageIsProductizedAndCarinaOnly(t *testing.T) {
	for _, want := range []string{"Start and run:", "Inspect sessions:", "Audit and rollback:", "Providers and BYOK:", "Native tools, no daemon:"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage missing section %q", want)
		}
	}
	if strings.Contains(usage, " pi ") || strings.Contains(usage, "PI_") {
		t.Fatalf("usage must not expose historical aliases:\n%s", usage)
	}
}

func TestUsageIncludesGatewayWSProbe(t *testing.T) {
	if !strings.Contains(usage, "carina gateway ws-probe <ws-url> [role]") {
		t.Fatalf("usage missing gateway ws-probe:\n%s", usage)
	}
}

func TestUsageIncludesMemoryCommands(t *testing.T) {
	for _, want := range []string{
		"Memory:",
		"carina memory status <session_id>",
		"carina memory write <session_id> <memory|user> add <content|->",
	} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage missing %q:\n%s", want, usage)
		}
	}
}

func TestMemoryRPCBuildsStatusAndWrite(t *testing.T) {
	method, params, err := memoryRPC([]string{"status", "sess_1"}, func() (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if method != "memory.status" || params["session_id"] != "sess_1" {
		t.Fatalf("unexpected status rpc: %s %+v", method, params)
	}

	method, params, err = memoryRPC([]string{"write", "sess_1", "user", "add", "-"}, func() (string, error) {
		return "Remember the preferred editor.", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != "memory.write" || params["target"] != "user" || params["action"] != "add" || params["content"] != "Remember the preferred editor." {
		t.Fatalf("unexpected write rpc: %s %+v", method, params)
	}
}

func TestMemoryRPCRejectsIncompleteWrite(t *testing.T) {
	if _, _, err := memoryRPC([]string{"write", "sess_1", "memory", "remove"}, func() (string, error) { return "", nil }); err == nil {
		t.Fatal("incomplete remove should error")
	}
}

func TestGatewayWSProbePrintsHelloResponse(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "gateway.hello", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		var req rpc.HelloRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return map[string]any{"role": req.Role}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenWebSocket(addr, "/gateway", nil) }()
	defer s.Close()
	waitTCP(t, addr)

	out, err := captureStdout(t, func() error {
		return cmdGatewayWSProbe([]string{"ws://" + addr + "/gateway", "observer"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"jsonrpc": "2.0"`, `"id": 1`, `"role": "observer"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("ws-probe output missing %s:\n%s", want, out)
		}
	}
}

func TestGatewayWSProbeRequiresURL(t *testing.T) {
	if err := cmdGatewayWSProbe(nil); err == nil {
		t.Fatal("missing websocket url should error")
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String(), runErr
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tcp listener did not appear: %s", addr)
}
