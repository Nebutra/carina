package main

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

func TestHarnessBootstrapUsesOwnerEnsureThenBoundedIssue(t *testing.T) {
	oldResolve, oldConnect := resolveHarnessRuntime, connectHarnessRuntime
	t.Cleanup(func() { resolveHarnessRuntime, connectHarnessRuntime = oldResolve, oldConnect })
	resolveHarnessRuntime = func(_, _ string, _ localruntime.Mode) (localruntime.Resolution, error) {
		return localruntime.Resolution{Workspace: localruntime.Workspace{CanonicalRoot: "/tmp/harness-workspace"}}, nil
	}
	clientConn, serverConn := net.Pipe()
	client := rpc.NewClient(clientConn, clientConn, clientConn)
	connectHarnessRuntime = func(localruntime.Spec) (*rpc.Client, localdaemon.RuntimeDescription, error) {
		return client, localdaemon.RuntimeDescription{}, nil
	}
	requests := make(chan string, 2)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		responses := []map[string]any{
			{"gateway_url": "ws://127.0.0.1:34567/gateway"},
			{"token": "gw1.test.token", "claims": map[string]any{"exp": time.Now().Add(5 * time.Minute).Unix()}},
		}
		for i, result := range responses {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params map[string]any  `json:"params"`
			}
			if json.Unmarshal(line, &req) != nil {
				return
			}
			requests <- req.Method
			if i == 1 {
				if req.Params["tenant_id"] != "local" || req.Params["transport"] != "ws" {
					return
				}
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = serverConn.Write(append(raw, '\n'))
		}
	}()

	out, err := captureStdout(t, func() error {
		return cmdHarness([]string{"bootstrap", "--workspace", "/tmp/harness-workspace", "--role", "operator", "--origin", "http://127.0.0.1:4173", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var got harnessBootstrapResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bootstrap output is not JSON: %v\n%s", err, out)
	}
	if got.WorkspaceRoot != "/tmp/harness-workspace" || got.GatewayURL == "" || got.Token != "gw1.test.token" || got.Role != "operator" || got.ExpiresAt.IsZero() {
		t.Fatalf("unexpected bootstrap result: %+v", got)
	}
	if first, second := <-requests, <-requests; first != "gateway.local.ensure" || second != "gateway.token.issue" {
		t.Fatalf("bootstrap call order = %q, %q", first, second)
	}
}

func TestHarnessBootstrapRejectsUnsafeInvocation(t *testing.T) {
	for _, args := range [][]string{
		{"bootstrap", "--workspace", "/tmp/ws", "--role", "observer", "--origin", "https://evil.example", "--json"},
		{"bootstrap", "--workspace", "/tmp/ws", "--role", "admin", "--origin", "http://127.0.0.1:4173", "--json"},
		{"bootstrap", "--workspace", "/tmp/ws", "--role", "observer", "--origin", "http://127.0.0.1:4173"},
	} {
		if err := cmdHarness(args); err == nil || strings.Contains(err.Error(), "gw1.") {
			t.Fatalf("unsafe invocation %v returned %v", args, err)
		}
	}
}
