package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

// nativeToolsContract is envelope C for HTTP function-calling. Schemas travel
// in the request tools array, not in this string. Mode, Identity, and Intent
// stay on the named layers. Do not restate JSON ReAct or paste toolsHelp.
const nativeToolsContract = `Call native functions with the same names and fields as Carina actions. One call per turn except a parallel list/read/search batch. Use done when the task is finished.`

func objectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func carinaToolSpecs() []modelrouter.ToolSpec {
	intent := stringProp("brief user-visible purpose")
	return []modelrouter.ToolSpec{
		{Name: "list", Description: "list the workspace file tree", Parameters: objectSchema([]string{"intent"}, map[string]any{"intent": intent})},
		{Name: "read", Description: "read a workspace file", Parameters: objectSchema([]string{"path", "intent"}, map[string]any{"path": stringProp("workspace-relative path"), "intent": intent})},
		{Name: "search", Description: "search the workspace", Parameters: objectSchema([]string{"pattern", "intent"}, map[string]any{"pattern": stringProp("search text"), "intent": intent})},
		{Name: "web.fetch", Description: "fetch public text or JSON over HTTPS after host approval", Parameters: objectSchema([]string{"url", "intent"}, map[string]any{"url": stringProp("https URL"), "intent": intent})},
		{Name: "web.search", Description: "search the public web after host approval", Parameters: objectSchema([]string{"query", "intent"}, map[string]any{"query": stringProp("search query"), "intent": intent})},
		{Name: "run", Description: "run a workspace-scoped, policy-gated command", Parameters: objectSchema([]string{"intent"}, map[string]any{"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "intent": intent})},
		{Name: "patch", Description: "propose and apply a complete-file transactional write", Parameters: objectSchema([]string{"path", "content", "intent"}, map[string]any{"path": stringProp("workspace-relative path"), "content": stringProp("complete new file content"), "intent": intent})},
		{Name: "edit", Description: "replace one unique exact span in a previously read file", Parameters: objectSchema([]string{"path", "old", "new", "intent"}, map[string]any{"path": stringProp("workspace-relative path"), "old": stringProp("exact unique span to replace"), "new": stringProp("replacement text"), "intent": intent})},
		{Name: "memory", Description: "update governed long-term memory", Parameters: objectSchema([]string{"intent"}, map[string]any{"target": stringProp("memory or user"), "action": stringProp("add, replace, remove, or batch"), "content": stringProp("fact"), "old_text": stringProp("unique substring"), "intent": intent})},
		{Name: "ask_user", Description: "pause for a structured operator choice or free-text reply", Parameters: objectSchema([]string{"prompt", "intent"}, map[string]any{"prompt": stringProp("question for the operator"), "intent": intent})},
		{Name: "todo", Description: "replace the session checklist", Parameters: objectSchema([]string{"intent"}, map[string]any{
			"todos": map[string]any{
				"type": "array",
				"items": objectSchema(nil, map[string]any{
					"content": stringProp("checklist item"),
					"status":  stringProp("pending, in_progress, or completed"),
				}),
			},
			"intent": intent,
		})},
		{Name: "update_plan", Description: "replace the session checklist (alias of todo)", Parameters: objectSchema([]string{"intent"}, map[string]any{
			"plan": map[string]any{
				"type": "array",
				"items": objectSchema(nil, map[string]any{
					"step":   stringProp("checklist item"),
					"status": stringProp("pending, in_progress, or completed"),
				}),
			},
			"intent": intent,
		})},
		{Name: "code.search", Description: "ranked code search", Parameters: objectSchema([]string{"query", "intent"}, map[string]any{"query": stringProp("free text or identifier"), "intent": intent})},
		{Name: "code.symbols", Description: "definitions and references", Parameters: objectSchema([]string{"name", "intent"}, map[string]any{"name": stringProp("symbol name"), "intent": intent})},
		{Name: "code.map", Description: "compact ranked repository map", Parameters: objectSchema([]string{"intent"}, map[string]any{"intent": intent})},
		{Name: "code.def", Description: "precise definition", Parameters: objectSchema([]string{"name", "intent"}, map[string]any{"name": stringProp("symbol name"), "intent": intent})},
		{Name: "code.refs", Description: "precise references", Parameters: objectSchema([]string{"name", "intent"}, map[string]any{"name": stringProp("symbol name"), "intent": intent})},
		{Name: "code.impact", Description: "bounded transitive dependents of a symbol", Parameters: objectSchema([]string{"name", "intent"}, map[string]any{"name": stringProp("symbol name"), "intent": intent})},
		{Name: "spawn", Description: "delegate work to a subagent", Parameters: objectSchema([]string{"intent"}, map[string]any{"agent": stringProp("agent name"), "task": stringProp("task for the child"), "intent": intent})},
		{Name: "workflow", Description: "run a named workflow DAG", Parameters: objectSchema([]string{"workflow", "intent"}, map[string]any{"workflow": stringProp("workflow name"), "task": stringProp("optional input"), "intent": intent})},
		{Name: "mcp", Description: "call a connected MCP tool", Parameters: objectSchema([]string{"mcp_server", "mcp_tool", "intent"}, map[string]any{"mcp_server": stringProp("server id"), "mcp_tool": stringProp("tool name"), "intent": intent})},
		{Name: "mcp_find", Description: "search connected MCP tools", Parameters: objectSchema([]string{"query", "intent"}, map[string]any{"query": stringProp("free text"), "intent": intent})},
		{Name: "done", Description: "finish the task with the operator-visible summary", Parameters: objectSchema([]string{"summary"}, map[string]any{"summary": stringProp("plain-language final answer")})},
	}
}

func decodeNativeToolCalls(calls []modelrouter.ToolCall) (action, error) {
	if len(calls) == 0 {
		return action{}, fmt.Errorf("no native tool calls")
	}
	if len(calls) == 1 {
		return decodeNativeToolCall(calls[0])
	}
	batch := action{}
	for i, call := range calls {
		sub, err := decodeNativeToolCall(call)
		if err != nil {
			return action{}, fmt.Errorf("native tool %d: %w", i, err)
		}
		if len(sub.Actions) > 0 {
			return action{}, fmt.Errorf("nested batches not allowed")
		}
		batch.Actions = append(batch.Actions, sub)
	}
	if bad := nonParallelBatchTools(batch.Actions); len(bad) > 0 {
		return action{}, fmt.Errorf("mixed native tool set: %s", strings.Join(bad, ", "))
	}
	return batch, nil
}

func nativeToolCallsAuditText(calls []modelrouter.ToolCall) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		args := strings.TrimSpace(string(call.Arguments))
		if args == "" {
			args = "{}"
		}
		parts = append(parts, fmt.Sprintf(`{"tool":%q,"arguments":%s}`, call.Name, args))
	}
	return strings.Join(parts, "\n")
}

func decodeNativeToolCall(call modelrouter.ToolCall) (action, error) {
	fields := map[string]any{}
	if len(call.Arguments) > 0 && string(call.Arguments) != "null" {
		if err := json.Unmarshal(call.Arguments, &fields); err != nil {
			return action{}, fmt.Errorf("native arguments: %w", err)
		}
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["tool"] = call.Name
	raw, err := json.Marshal(fields)
	if err != nil {
		return action{}, err
	}
	return decodeAction(raw)
}

func catalogModelToolCall(cat provider.Catalog, model string) bool {
	providerID, modelID, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok || providerID == "" || modelID == "" {
		return false
	}
	info, ok := cat[providerID]
	if !ok {
		return false
	}
	entry, ok := info.Models[modelID]
	if !ok {
		return false
	}
	return entry.ToolCall
}

func (d *Daemon) nativeToolsEligible(reasoner Reasoner, model string) bool {
	if d == nil || !d.nativeToolsHTTP {
		return false
	}
	if !catalogModelToolCall(d.providerCatalog, model) {
		return false
	}
	rr, ok := reasoner.(*routerReasoner)
	if !ok {
		return false
	}
	providerID, _, cut := strings.Cut(strings.TrimSpace(model), "/")
	if cut && strings.EqualFold(providerID, provider.GrokBuildProviderID) {
		return false
	}
	if _, _, ok := rr.claudeCodeRoute(model); ok {
		return false
	}
	return true
}
