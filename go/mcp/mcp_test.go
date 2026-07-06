package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockServerPy is a minimal MCP server over stdio JSON-RPC: it answers
// initialize / tools/list / tools/call, exposing one "echo" tool.
const mockServerPy = `import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"mock"},"capabilities":{}}})+"\n")
        sys.stdout.flush()
    elif method == "tools/list":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"tools":[{"name":"echo","description":"echoes arguments","inputSchema":{"type":"object"}}]}})+"\n")
        sys.stdout.flush()
    elif method == "tools/call":
        args = msg.get("params", {}).get("arguments", {})
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"content":[{"type":"text","text":"ECHO:"+json.dumps(args)}]}})+"\n")
        sys.stdout.flush()
    elif method and method.startswith("notifications/"):
        pass
    elif mid is not None:
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"error":{"code":-32601,"message":"method not found"}})+"\n")
        sys.stdout.flush()
`

func writeMockConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "mock_mcp.py")
	if err := os.WriteFile(script, []byte(mockServerPy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "mcp.json")
	conf := `{"mcpServers":{"mock":{"command":"python3","args":["` + script + `"]}}}`
	if err := os.WriteFile(cfg, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMCPClientLifecycle(t *testing.T) {
	cfg := writeMockConfig(t)
	m := NewManager()
	defer m.Close()
	m.LoadAndConnect(cfg)

	if got := m.Servers(); len(got) != 1 || got[0] != "mock" {
		t.Fatalf("expected [mock] connected, got %v", got)
	}
	found := false
	for _, tl := range m.Tools() {
		if tl.Server == "mock" && tl.Name == "echo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("echo tool not discovered: %v", m.Tools())
	}

	out, err := m.Call("mock", "echo", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ECHO") || !strings.Contains(out, `"x"`) {
		t.Fatalf("unexpected tool result: %q", out)
	}

	if _, err := m.Call("nope", "echo", nil); err == nil {
		t.Fatal("unknown server should error")
	}
}
