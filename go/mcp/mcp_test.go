package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockServerPy is a minimal MCP server over stdio JSON-RPC: it answers
// initialize / tools/list / tools/call plus prompts/list / prompts/get.
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
    elif method == "prompts/list":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"prompts":[{"name":"review","description":"review a target","arguments":[{"name":"target","description":"thing to review","required":True}]}]}})+"\n")
        sys.stdout.flush()
    elif method == "prompts/get":
        params = msg.get("params", {})
        args = params.get("arguments", {})
        text = "Review target: " + args.get("target", "")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"messages":[{"role":"user","content":{"type":"text","text":text}}]}})+"\n")
        sys.stdout.flush()
    elif method and method.startswith("notifications/"):
        pass
    elif mid is not None:
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"error":{"code":-32601,"message":"method not found"}})+"\n")
        sys.stdout.flush()
`

const promptOnlyServerPy = `import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"prompt-only"},"capabilities":{}}})+"\n")
        sys.stdout.flush()
    elif method == "prompts/list":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"prompts":[{"name":"brief","description":"brief prompt"}]}})+"\n")
        sys.stdout.flush()
    elif method == "prompts/get":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"messages":[{"role":"user","content":"brief text"}]}})+"\n")
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
	var prompt *Prompt
	for _, p := range m.Prompts() {
		if p.Server == "mock" && p.Name == "review" {
			cp := p
			prompt = &cp
		}
	}
	if prompt == nil || len(prompt.Arguments) != 1 || !prompt.Arguments[0].Required {
		t.Fatalf("review prompt not discovered: %+v", m.Prompts())
	}

	out, err := m.Call("mock", "echo", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ECHO") || !strings.Contains(out, `"x"`) {
		t.Fatalf("unexpected tool result: %q", out)
	}
	promptOut, err := m.GetPrompt("mock", "review", map[string]string{"target": "parser"})
	if err != nil {
		t.Fatal(err)
	}
	if promptOut != "Review target: parser" {
		t.Fatalf("unexpected prompt result: %q", promptOut)
	}

	if _, err := m.Call("nope", "echo", nil); err == nil {
		t.Fatal("unknown server should error")
	}
}

func TestPromptOnlyMCPServerConnects(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "prompt_only.py")
	if err := os.WriteFile(script, []byte(promptOnlyServerPy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "mcp.json")
	conf := `{"mcpServers":{"promptOnly":{"command":"python3","args":["` + script + `"]}}}`
	if err := os.WriteFile(cfg, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	defer m.Close()
	m.LoadAndConnect(cfg)

	if got := m.Tools(); len(got) != 0 {
		t.Fatalf("prompt-only server should expose no tools, got %v", got)
	}
	prompts := m.Prompts()
	if len(prompts) != 1 || prompts[0].Server != "promptOnly" || prompts[0].Name != "brief" {
		t.Fatalf("prompt-only server prompt not discovered: %+v", prompts)
	}
	out, err := m.GetPrompt("promptOnly", "brief", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "brief text" {
		t.Fatalf("unexpected prompt-only render: %q", out)
	}
}

func TestPrivateMCPServerHiddenFromPublicSurface(t *testing.T) {
	cfg := writeMockConfig(t)
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var conf config
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	if err := m.ConnectPrivate("private", conf.MCPServers["mock"]); err != nil {
		t.Fatal(err)
	}
	if tools := m.Tools(); len(tools) != 0 {
		t.Fatalf("private tools should be hidden: %+v", tools)
	}
	if _, err := m.CallPublic("private", "echo", map[string]any{"x": 1}); err == nil {
		t.Fatal("public call should reject private server")
	}
	out, err := m.Call("private", "echo", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"x"`) {
		t.Fatalf("unexpected private call output: %q", out)
	}
}
