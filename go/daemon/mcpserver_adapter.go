package daemon

import (
	"context"
	"fmt"
	"io"

	"github.com/Nebutra/carina/go/mcpserver"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const mcpServerVersion = "1.0.0"

// strProp / arrProp / objSchema build minimal JSON Schemas for the tool catalog.
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func arrProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

func objSchema(props map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	if props == nil {
		s["properties"] = map[string]any{}
	}
	return s
}

// carinaToolCatalog is the set of Carina tools exposed over MCP server mode.
// Each maps onto the same kernel-gated action the agent loop uses.
var carinaToolCatalog = []mcpserver.Tool{
	{Name: "list", Description: "List the workspace file tree.", InputSchema: objSchema(nil, nil)},
	{Name: "read", Description: "Read a file (capability-gated).",
		InputSchema: objSchema(map[string]any{"path": strProp("workspace-relative file path")}, []string{"path"})},
	{Name: "search", Description: "Search the workspace for a pattern.",
		InputSchema: objSchema(map[string]any{"pattern": strProp("text/regex to find")}, []string{"pattern"})},
	{Name: "run", Description: "Run a command (OS-sandboxed + policy-gated; risky commands are denied).",
		InputSchema: objSchema(map[string]any{"command": arrProp("argv, e.g. [\"ls\",\"-la\"]")}, []string{"command"})},
	{Name: "patch", Description: "Propose+apply a full-file edit (transactional, rollbackable, capability-gated).",
		InputSchema: objSchema(map[string]any{
			"path":    strProp("workspace-relative file path"),
			"content": strProp("FULL new file content"),
		}, []string{"path", "content"})},
}

// actionFromMCP maps an MCP tool call onto an agent action. Returns nil for an
// unknown tool.
func actionFromMCP(name string, args map[string]any) *action {
	str := func(k string) string { s, _ := args[k].(string); return s }
	switch name {
	case "list":
		return &action{Tool: "list"}
	case "read":
		return &action{Tool: "read", Path: str("path")}
	case "search":
		return &action{Tool: "search", Pattern: str("pattern")}
	case "patch":
		return &action{Tool: "patch", Path: str("path"), Content: str("content")}
	case "run":
		var cmd []string
		if raw, ok := args["command"].([]any); ok {
			for _, c := range raw {
				if s, ok := c.(string); ok {
					cmd = append(cmd, s)
				}
			}
		}
		return &action{Tool: "run", Command: cmd}
	default:
		return nil
	}
}

// carinaMCPHandler exposes one session's tools to an MCP client. Every call is
// routed through executeAction, so the capability kernel, lifecycle hooks, and
// plan-mode gate all apply exactly as they do for the agent loop.
type carinaMCPHandler struct {
	d    *Daemon
	sess *sessionstore.Session
	task *scheduler.Task
}

func (h *carinaMCPHandler) Tools() []mcpserver.Tool { return carinaToolCatalog }

func (h *carinaMCPHandler) Call(name string, args map[string]any) (string, error) {
	act := actionFromMCP(name, args)
	if act == nil {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return h.d.executeAction(h.sess, h.task, act), nil
}

// ServeMCP runs Carina as an MCP server for one session over the given streams,
// exposing its kernel-gated tools to an external MCP client. It blocks until the
// input stream closes or ctx is cancelled.
func (d *Daemon) ServeMCP(ctx context.Context, sessionID string, in io.Reader, out io.Writer) error {
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return fmt.Errorf("mcpserver: unknown session %s", sessionID)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "mcp server session")
	h := &carinaMCPHandler{d: d, sess: sess, task: task}
	return mcpserver.New("carina", mcpServerVersion, h).Serve(ctx, in, out)
}
