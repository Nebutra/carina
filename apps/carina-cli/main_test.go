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

func TestValidateCLIEventSchema(t *testing.T) {
	for _, v := range []string{"0.3.0", "0.3.9", "v0.3.1"} {
		if err := validateCLIEventSchema(map[string]any{"capabilities": map[string]any{"event_schema_version": v}}); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
	for _, v := range []string{"", "0.2.9", "0.4.0", "1.3.0"} {
		if err := validateCLIEventSchema(map[string]any{"capabilities": map[string]any{"event_schema_version": v}}); err == nil {
			t.Fatalf("accepted %q", v)
		}
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
	if !strings.Contains(usage, "carina gateway ws-probe <ws-url> [role] [token]") {
		t.Fatalf("usage missing gateway ws-probe:\n%s", usage)
	}
}

func TestUsageIncludesBackpressureAndDebugCommands(t *testing.T) {
	for _, want := range []string{
		"carina backpressure status",
		"carina debug snapshot [limit]",
		"carina debug trace <correlation_id> [limit]",
	} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage missing %q:\n%s", want, usage)
		}
	}
}

func TestUsageIncludesMemoryCommands(t *testing.T) {
	for _, want := range []string{
		"Memory:",
		"carina memory status <session_id>",
		"carina memory write <session_id> <memory|user> add <content|->",
		"carina memory projection-authorize <session_id>",
		"carina memory projection-retry <session_id> <document_id>",
		"carina memory projection-reseed <session_id> <document_id> --remote-quiesced",
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
	for _, want := range []string{
		"carina resume <session_id> [prompt|-]",
		"carina steer <task_id> <message>",
		"carina answer <question_id> <value>",
		"carina fork <session_id>",
		"carina cost [session_id] [--json]",
		"carina workers",
		"carina daemon start",
		"carina completion <bash|zsh|fish>",
	} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage missing productized command %q:\n%s", want, usage)
		}
	}
}

func TestRunAnswerResolvesStructuredQuestion(t *testing.T) {
	oldDial := dialHook
	t.Cleanup(func() { dialHook = oldDial })
	s := rpc.NewServer()
	var got map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "task.user.answer", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &got); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() { _ = s.ListenTCP(addr) }()
	t.Cleanup(func() { _ = s.Close() })
	waitTCP(t, addr)
	dialHook = func() (*rpcClient, error) { return rpc.DialTCP(addr) }

	out, err := captureStdout(t, func() error { return run("answer", []string{"question_1", "yes"}) })
	if err != nil {
		t.Fatal(err)
	}
	if got["question_id"] != "question_1" || got["value"] != "yes" {
		t.Fatalf("unexpected answer params: %#v", got)
	}
	if !strings.Contains(out, `"accepted": true`) {
		t.Fatalf("answer output missing acknowledgement: %s", out)
	}
	if err := run("answer", []string{"question_1"}); err == nil {
		t.Fatal("answer without a value must fail")
	}
}

func TestCmdSteerQueuesMessage(t *testing.T) {
	s := rpc.NewServer()
	var got map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "task.steer", Scope: rpc.ScopeWrite, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &got); err != nil {
			return nil, err
		}
		return map[string]any{"queued": true}, nil
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
		return cmdSteer(c, []string{"task_1", "also", "add", "tests"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["task_id"] != "task_1" || got["message"] != "also add tests" {
		t.Fatalf("unexpected task.steer params: %#v", got)
	}
	if !strings.Contains(out, `"queued": true`) {
		t.Fatalf("steer output missing acknowledgement:\n%s", out)
	}
	if err := cmdSteer(c, []string{"task_1"}); err == nil {
		t.Fatal("missing steering message should fail")
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
	if _, err := captureStdout(t, func() error { return cmdContext(c, []string{"retrieve", "abc"}) }); err != nil {
		t.Fatal(err)
	}
	if retrieved["hash"] != "abc" || retrieved["query"] != nil {
		t.Fatalf("retrieve params = %#v", retrieved)
	}
	if err := cmdContext(c, []string{"retrieve", "abc", "needle"}); err == nil {
		t.Fatal("retrieve query must be rejected before RPC because the managed Headroom contract is hash-only")
	}
}

func TestCmdBackpressureStatus(t *testing.T) {
	s := rpc.NewServer()
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "backpressure.status", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		return map[string]any{"ttl_seconds": 30, "reports": []any{}, "directives": []any{}}, nil
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

	out, err := captureStdout(t, func() error { return cmdBackpressure(c, []string{"status"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"ttl_seconds": 30`) {
		t.Fatalf("backpressure status output missing ttl:\n%s", out)
	}
}

func TestCmdDebugSnapshotAndTrace(t *testing.T) {
	s := rpc.NewServer()
	var snapshotParams map[string]any
	var traceParams map[string]any
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "debug.snapshot", Scope: rpc.ScopeAdmin, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &snapshotParams); err != nil {
			return nil, err
		}
		return map[string]any{"enabled": true, "events": []any{map[string]any{"component": "scheduler"}}, "capacity": 4096}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(rpc.MethodDescriptor{Method: "debug.correlation.search", Scope: rpc.ScopeAdmin, Remote: true}, func(params json.RawMessage) (any, error) {
		if err := json.Unmarshal(params, &traceParams); err != nil {
			return nil, err
		}
		return map[string]any{"enabled": true, "correlation_id": traceParams["correlation_id"], "events": []any{}}, nil
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

	if out, err := captureStdout(t, func() error { return cmdDebug(c, []string{"snapshot", "2"}) }); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, `"enabled": true`) {
		t.Fatalf("debug snapshot output missing enabled:\n%s", out)
	}
	if snapshotParams["limit"].(float64) != 2 {
		t.Fatalf("snapshot limit not forwarded: %+v", snapshotParams)
	}
	if _, err := captureStdout(t, func() error { return cmdDebug(c, []string{"trace", "task_1", "5"}) }); err != nil {
		t.Fatal(err)
	}
	if traceParams["correlation_id"] != "task_1" || traceParams["limit"].(float64) != 5 {
		t.Fatalf("trace params not forwarded: %+v", traceParams)
	}
	if err := cmdDebug(c, []string{"snapshot", "0"}); err == nil {
		t.Fatal("non-positive debug limit should error")
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
	method, params, err = memoryRPC([]string{"projection-authorize", "sess_1"}, func() (string, error) { return "", nil })
	if err != nil || method != "memory.projection.authorize" || params["session_id"] != "sess_1" {
		t.Fatalf("unexpected projection authorize rpc: %s %+v %v", method, params, err)
	}
	method, params, err = memoryRPC([]string{"projection-retry", "sess_1", "mem_1"}, func() (string, error) { return "", nil })
	if err != nil || method != "memory.projection.retry" || params["document_id"] != "mem_1" {
		t.Fatalf("unexpected projection retry rpc: %s %+v %v", method, params, err)
	}
	if _, _, err = memoryRPC([]string{"projection-reseed", "sess_1", "mem_1"}, func() (string, error) { return "", nil }); err == nil {
		t.Fatal("projection reseed accepted without --remote-quiesced")
	}
	method, params, err = memoryRPC([]string{"projection-reseed", "sess_1", "mem_1", "--remote-quiesced"}, func() (string, error) { return "", nil })
	if err != nil || method != "memory.projection.reseed" || params["remote_quiesced"] != true {
		t.Fatalf("unexpected projection reseed rpc: %s %+v %v", method, params, err)
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
	issuer, err := rpc.NewGatewayTokenIssuer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := issuer.Issue("cli-test", rpc.RoleObserver, []rpc.Scope{rpc.ScopeRead}, time.Minute, "ws")
	if err != nil {
		t.Fatal(err)
	}
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
	go func() {
		_ = s.ListenWebSocketWithOptions(addr, rpc.WebSocketOptions{Path: "/gateway", TokenVerifier: issuer})
	}()
	defer s.Close()
	waitTCP(t, addr)

	out, err := captureStdout(t, func() error {
		return cmdGatewayWSProbe([]string{"ws://" + addr + "/gateway", "observer", token})
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
