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

func carinaToolSpecs() []modelrouter.ToolSpec {
	return defaultBuiltinTools.nativeToolSpecs()
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
