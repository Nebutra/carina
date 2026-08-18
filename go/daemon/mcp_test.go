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
	registry := listRes.(map[string]any)
	if revision, _ := registry["revision"].(string); revision == "" {
		t.Fatal("command.list must return a registry revision")
	}
	commands := registry["commands"].([]CommandInfo)
	var found *CommandInfo
	for _, cmd := range commands {
		if cmd.Name == "mcp.mock.review" {
			cp := cmd
			found = &cp
		}
	}
	if found == nil || found.ID != "prompt:mcp:mcp.mock.review" || found.Kind != "prompt_template" || found.Source != "mcp" || len(found.Arguments) != 1 || found.Arguments[0].Name != "target" {
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
	task := res.(*scheduler.ExecutionRun)
	if task.Agent != "general" || task.Model != "openai/gpt-5" {
		t.Fatalf("explicit agent/model not preserved: %+v", task)
	}
	if task.UserPrompt != "MCP review target: parser subsystem" {
		t.Fatalf("mcp prompt not expanded: %q", task.UserPrompt)
	}
}

func TestCommandListReportsProbingBeforeConnect(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	dir := t.TempDir()
	script := filepath.Join(dir, "mock.py")
	if err := os.WriteFile(script, []byte(mockMCPServerPy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(cfg, []byte(`{"mcpServers":{"mock":{"command":"python3","args":["`+script+`"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !d.mcp.BeginDeferredLoad(cfg) {
		t.Fatal("configured mcp.json must start probing")
	}
	before, err := d.handleCommandList(mustJSON(t, map[string]any{"workspace_root": ws}))
	if err != nil {
		t.Fatal(err)
	}
	reg := before.(map[string]any)
	if reg["state"] != "probing" {
		t.Fatalf("first command.list state = %#v", reg["state"])
	}
	if gen, _ := reg["generation"].(uint64); gen != 0 {
		t.Fatalf("probing generation = %#v", reg["generation"])
	}
	for _, cmd := range reg["commands"].([]CommandInfo) {
		if cmd.Source == "mcp" {
			t.Fatalf("unready MCP command presented as executable: %+v", cmd)
		}
	}
	inv, err := d.handleMCPInventory(mustJSON(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if inv.(map[string]any)["state"] != "probing" {
		t.Fatalf("mcp.inventory while probing = %#v", inv)
	}

	d.mcp.LoadAndConnect(cfg)
	after, err := d.handleCommandList(mustJSON(t, map[string]any{"workspace_root": ws}))
	if err != nil {
		t.Fatal(err)
	}
	ready := after.(map[string]any)
	if ready["state"] != "ready" {
		t.Fatalf("post-connect command.list state = %#v", ready["state"])
	}
	if gen, _ := ready["generation"].(uint64); gen != 1 {
		t.Fatalf("post-connect generation = %#v", ready["generation"])
	}
	found := false
	for _, cmd := range ready["commands"].([]CommandInfo) {
		if cmd.Name == "mcp.mock.review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("connected MCP prompt missing: %+v", ready["commands"])
	}
}

func TestMCPSlashWhileProbingDoesNotStartTurn(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	dir := t.TempDir()
	script := filepath.Join(dir, "mock.py")
	if err := os.WriteFile(script, []byte(mockMCPServerPy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(cfg, []byte(`{"mcpServers":{"mock":{"command":"python3","args":["`+script+`"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !d.mcp.BeginDeferredLoad(cfg) {
		t.Fatal("configured mcp.json must start probing")
	}
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	_, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "/mcp.mock.review parser subsystem",
	}))
	if err == nil || !strings.Contains(err.Error(), "probing") {
		t.Fatalf("probing MCP slash must fail closed, err=%v", err)
	}
	for _, task := range d.sched.List() {
		if task.SessionID == sess.SessionID {
			t.Fatalf("probing slash started a turn: %+v", task)
		}
	}
}
