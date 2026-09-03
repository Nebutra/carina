package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Nebutra/carina/go/mcpserver"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const mcpServerVersion = "1.0.0"

// carinaToolCatalog is the set of Carina tools exposed over MCP server mode.
// Each is projected from the builtin descriptor registry and maps onto the
// same normalized action and governed handler the agent loop uses.
var carinaToolCatalog []mcpserver.Tool

func externalMCPToolCatalog(registry *builtinToolRegistry) []mcpserver.Tool {
	if registry == nil {
		return nil
	}
	tools := make([]mcpserver.Tool, 0, len(registry.ordered))
	for _, descriptor := range registry.ordered {
		if !descriptor.ExternalMCP {
			continue
		}
		schema := cloneStringAnyMap(descriptor.Schema)
		properties, _ := schema["properties"].(map[string]any)
		delete(properties, "intent")
		required := schemaStringSlice(schema["required"])
		filtered := required[:0]
		for _, name := range required {
			if name != "intent" {
				filtered = append(filtered, name)
			}
		}
		if len(filtered) == 0 {
			delete(schema, "required")
		} else {
			schema["required"] = filtered
		}
		tools = append(tools, mcpserver.Tool{
			Name: descriptor.Name, Description: descriptor.ExternalMCPDescription, InputSchema: schema,
		})
	}
	return tools
}

// actionFromMCP maps an MCP tool call onto an agent action. Returns nil for an
// unknown tool.
func actionFromMCP(name string, args map[string]any) *action {
	descriptor, ok := defaultBuiltinTools.lookup(name)
	if !ok || !descriptor.ExternalMCP {
		return nil
	}
	fields := make(map[string]any, len(args)+1)
	for key, value := range args {
		fields[key] = value
	}
	fields["tool"] = name
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	act, err := decodeAction(raw)
	if err != nil {
		return nil
	}
	return &act
}

// carinaMCPHandler exposes one session's tools to an MCP client. Every call is
// routed through executeAction, so the capability kernel, lifecycle hooks, and
// plan-mode gate all apply exactly as they do for the agent loop.
type carinaMCPHandler struct {
	d    *Daemon
	sess *sessionstore.Session
	task *scheduler.ExecutionRun
}

func (h *carinaMCPHandler) Tools() []mcpserver.Tool {
	if h != nil && h.d != nil {
		return externalMCPToolCatalog(h.d.builtinToolRegistry())
	}
	return externalMCPToolCatalog(defaultBuiltinTools)
}

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
