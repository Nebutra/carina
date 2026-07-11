package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// mcpFindLimit caps how many ranked matches one mcp_find call returns; each
// match carries a full input schema, so the observation stays bounded.
const mcpFindLimit = 6

// mcpFindOutcome handles the mcp_find action: a read-only, stateless search
// over the connected public MCP servers' tool metadata (mcp.Manager.SearchTools,
// which excludes hidden/private servers exactly like Tools()). Unlike the
// always-injected MCP TOOLS index (bounded descriptions, no schemas), results
// here include each match's full input schema so the model can construct
// correct arguments before spending a real, PluginLoad-gated mcp call.
// Execution gating is unchanged: finding a tool grants nothing — invoking it
// still goes through callMCPOutcome's kernel decision + audit. mcp_find itself
// is audited by the standard tool lifecycle envelopes (ToolCallRequested with
// the query, then ToolCallCompleted/Failed) in executeActionOutcome.
func (d *Daemon) mcpFindOutcome(sess *sessionstore.Session, task *scheduler.Task, act *action) toolExecutionOutcome {
	query := strings.TrimSpace(act.Query)
	if query == "" {
		return toolFailed("error: mcp_find needs query", "invalid_arguments")
	}
	matches := d.mcp.SearchTools(query, mcpFindLimit)
	if len(matches) == 0 {
		return toolCompleted(fmt.Sprintf("no MCP tools matched %q. The server may be disconnected or not configured, "+
			"or the tool may be named differently — try broader keywords, or check the MCP TOOLS list in the system prompt.", query))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d MCP tool(s) matched %q — call via {\"tool\":\"mcp\",\"mcp_server\":\"<server>\",\"mcp_tool\":\"<name>\",\"args\":{...}}:\n", len(matches), query)
	for _, m := range matches {
		fmt.Fprintf(&b, "\n- mcp__%s__%s: %s\n", m.Server, m.Name, m.Description)
		if schema := compactJSON(m.InputSchema); schema == "" {
			b.WriteString("  input schema: (none advertised)\n")
		} else {
			fmt.Fprintf(&b, "  input schema: %s\n", schema)
		}
	}
	return toolCompleted(strings.TrimSpace(b.String()))
}

// compactJSON renders raw JSON on one line; malformed or empty input yields "".
func compactJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return buf.String()
}
