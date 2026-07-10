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

func TestUsageIncludesContextCommands(t *testing.T) {
	for _, want := range []string{
		"Context engine:",
		"carina context status",
		"carina context doctor",
	} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage missing %q:\n%s", want, usage)
		}
	}
}

func TestUsageIncludesResumeContinuation(t *testing.T) {
	if !strings.Contains(usage, "carina resume <session_id> [prompt|-]") {
		t.Fatalf("usage missing productized resume command:\n%s", usage)
	}
}

func TestParseResumeArgs(t *testing.T) {
	opts, err := parseResumeArgs([]string{"--model", "openai/gpt-5", "-a", "build", "--watch", "sess_1", "continue", "work"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.sessionID != "sess_1" || opts.prompt != "continue work" || opts.model != "openai/gpt-5" || opts.agent != "build" || !opts.watch {
		t.Fatalf("unexpected resume opts: %+v", opts)
	}
	if _, err := parseResumeArgs(nil); err == nil {
		t.Fatal("missing session id should error")
	}
}

func TestCmdResumeSubmitsTaskToExistingSession(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "session.get", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return map[string]any{
			"session_id":         "sess_1",
			"workspace_id":       "ws_1",
			"workspace_root":     "/tmp/ws",
			"status":             "active",
			"permission_profile": "safe-edit",
			"created_at":         "2026-07-08T00:00:00Z",
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var submitted map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "task.submit", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &submitted); err != nil {
			return nil, err
		}
		return map[string]any{"task_id": "task_1", "session_id": submitted["session_id"], "user_prompt": submitted["prompt"]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenTCP(addr) }()
	defer s.Close()
	waitTCP(t, addr)
	c, err := rpc.DialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	out, err := captureStdout(t, func() error {
		return cmdResume(c, []string{"sess_1", "--agent", "build", "continue please"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted["session_id"] != "sess_1" || submitted["prompt"] != "continue please" || submitted["agent"] != "build" {
		t.Fatalf("unexpected task.submit params: %+v", submitted)
	}
	for _, want := range []string{"resuming session: sess_1", `"task_id": "task_1"`, "To continue this session"} {
		if !strings.Contains(out, want) {
			t.Fatalf("resume output missing %q:\n%s", want, out)
		}
	}
}

func TestCmdContextStatus(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "context.status", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return map[string]any{"effective_engine": "noop"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenTCP(addr) }()
	defer s.Close()
	waitTCP(t, addr)
	c, err := rpc.DialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	out, err := captureStdout(t, func() error {
		return cmdContext(c, []string{"status"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"effective_engine": "noop"`) {
		t.Fatalf("context status output missing engine:\n%s", out)
	}
}

func TestCmdContextStatsCompressRetrieve(t *testing.T) {
	s := rpc.NewServer()
	var compressed string
	var retrieved map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "context.stats", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return map[string]any{"local": map[string]any{"engine": "noop"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "context.compress", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		var p map[string]any
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		compressed, _ = p["content"].(string)
		return map[string]any{"content": compressed}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "context.retrieve", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &retrieved); err != nil {
			return nil, err
		}
		return map[string]any{"ref": retrieved["hash"], "content": "original"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenTCP(addr) }()
	defer s.Close()
	waitTCP(t, addr)
	c, err := rpc.DialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if out, err := captureStdout(t, func() error { return cmdContext(c, []string{"stats"}) }); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, `"engine": "noop"`) {
		t.Fatalf("context stats output missing engine:\n%s", out)
	}
	if _, err := captureStdout(t, func() error { return cmdContext(c, []string{"compress", "hello"}) }); err != nil {
		t.Fatal(err)
	}
	if compressed != "hello" {
		t.Fatalf("compress params = %q", compressed)
	}
	if _, err := captureStdout(t, func() error { return cmdContext(c, []string{"retrieve", "abc", "needle"}) }); err != nil {
		t.Fatal(err)
	}
	if retrieved["hash"] != "abc" || retrieved["query"] != "needle" {
		t.Fatalf("retrieve params = %#v", retrieved)
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

	method, params, err = memoryRPC([]string{"search", "sess_1", "release", "checks"}, func() (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if method != "memory.search" || params["query"] != "release checks" {
		t.Fatalf("unexpected search rpc: %s %+v", method, params)
	}

	method, params, err = memoryRPC([]string{"search", "--semantic", "sess_1", "release", "checks"}, func() (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if method != "memory.search" || params["mode"] != "semantic" || params["query"] != "release checks" {
		t.Fatalf("unexpected semantic search rpc: %s %+v", method, params)
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
