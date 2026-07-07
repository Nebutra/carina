package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
)

// mockMCPServerPy is a minimal stdio MCP server exposing one "echo" tool and
// one "review" prompt.
const mockMCPServerPy = `import sys, json
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
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"tools":[{"name":"echo","description":"echoes arguments"}]}})+"\n")
        sys.stdout.flush()
    elif method == "tools/call":
        args = msg.get("params", {}).get("arguments", {})
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"content":[{"type":"text","text":"ECHO:"+json.dumps(args)}]}})+"\n")
        sys.stdout.flush()
    elif method == "prompts/list":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"prompts":[{"name":"review","description":"MCP review prompt","arguments":[{"name":"target","required":True}]}]}})+"\n")
        sys.stdout.flush()
    elif method == "prompts/get":
        args = msg.get("params", {}).get("arguments", {})
        text = "MCP review target: " + args.get("target", "")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"messages":[{"role":"user","content":{"type":"text","text":text}}]}})+"\n")
        sys.stdout.flush()
    elif method and method.startswith("notifications/"):
        pass
    elif mid is not None:
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"error":{"code":-32601,"message":"method not found"}})+"\n")
        sys.stdout.flush()
`

// TestMCPToolGatedAndProxied: an MCP tool call is gated by the capability kernel
// (PluginLoad) and proxied to the external server, returning its result.
func TestMCPToolGatedAndProxied(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	dir := t.TempDir()
	script := filepath.Join(dir, "mock.py")
	if err := os.WriteFile(script, []byte(mockMCPServerPy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "mcp.json")
	os.WriteFile(cfg, []byte(`{"mcpServers":{"mock":{"command":"python3","args":["`+script+`"]}}}`), 0o644)
	d.mcp.LoadAndConnect(cfg)

	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "x")

	obs := d.executeAction(sess, task, &action{Tool: "mcp", MCPServer: "mock", MCPTool: "echo", Args: map[string]any{"y": 2}})
	if !strings.Contains(obs, "ECHO") || !strings.Contains(obs, `"y"`) {
		t.Fatalf("mcp tool should proxy through the kernel gate and return the result, got: %s", obs)
	}
}

func TestMCPPromptListedAndExpandedAsSlashCommand(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	dir := t.TempDir()
	script := filepath.Join(dir, "mock.py")
	if err := os.WriteFile(script, []byte(mockMCPServerPy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "mcp.json")
	os.WriteFile(cfg, []byte(`{"mcpServers":{"mock":{"command":"python3","args":["`+script+`"]}}}`), 0o644)
	d.mcp.LoadAndConnect(cfg)

	listRes, err := d.handleCommandList(mustJSON(t, map[string]any{"workspace_root": ws}))
	if err != nil {
		t.Fatal(err)
	}
	commands := listRes.(map[string]any)["commands"].([]CommandInfo)
	var found *CommandInfo
	for _, cmd := range commands {
		if cmd.Name == "mcp.mock.review" {
			cp := cmd
			found = &cp
		}
	}
	if found == nil || found.Source != "mcp" || len(found.Arguments) != 1 || found.Arguments[0].Name != "target" {
		t.Fatalf("mcp prompt command not listed correctly: %+v", commands)
	}

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	res, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "/mcp.mock.review parser subsystem",
		"agent":      "general",
		"model":      "openai/gpt-5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := res.(*scheduler.Task)
	if task.Agent != "general" || task.Model != "openai/gpt-5" {
		t.Fatalf("explicit agent/model not preserved: %+v", task)
	}
	if task.UserPrompt != "MCP review target: parser subsystem" {
		t.Fatalf("mcp prompt not expanded: %q", task.UserPrompt)
	}
}
