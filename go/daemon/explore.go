package daemon

import (
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// exploreToolsHelp is the lean read-only contract for the built-in explore
// subagent. It must not advertise writes, shell, MCP, or spawn — those stay
// on the parent. Project instruction files are not a substitute for search.
const exploreToolsHelp = `Available tools:
- {"tool":"list"}                              list the workspace file tree
- {"tool":"read","path":"rel/path"}            read a file
- {"tool":"search","pattern":"text"}           search the workspace
- {"tool":"code.search","query":"free text or identifier"}      ranked code search
- {"tool":"code.symbols","name":"SymbolName"}                   definitions + references
- {"tool":"code.map"}                                           compact ranked repo map
- {"tool":"code.def","name":"SymbolName"}                       precise definition
- {"tool":"code.refs","name":"SymbolName"}                      precise references
- {"tool":"code.impact","name":"SymbolName"}                    bounded impact analysis
- {"tool":"done","summary":"exact paths and findings"}   finish and return to the parent

Harness protocol:
- Reply with ONLY the JSON object for the next action.
- Every tool action except "done" MUST include "intent":"<brief purpose>".
- Emit ONE tool action per turn, except a parallel batch of list/read/search.
- Do not edit files, run commands, call MCP, or spawn further agents.
- Do not read project instruction files or version-control status unless the task names that file.
- Return exact paths and findings. done.summary is the only text the parent sees.`

var exploreToolNames = []string{
	"list", "read", "search",
	"code.search", "code.symbols", "code.map", "code.def", "code.refs", "code.impact",
}

var exploreRestrictedTools = map[string]bool{
	"patch": true, "edit": true, "run": true, "memory": true,
	"spawn": true, "workflow": true, "mcp": true, "best_of_n": true,
	"web.fetch": true, "web.search": true, "ask_user": true,
}

func isExploreSubagent(spec *AgentSpec) bool {
	return spec != nil && spec.Name == "explore"
}

func (d *Daemon) resolveSubagentModel(spec *AgentSpec, parent *scheduler.ExecutionRun) string {
	if spec != nil {
		if model := strings.TrimSpace(spec.Model); model != "" {
			return model
		}
	}
	if !isExploreSubagent(spec) {
		return ""
	}
	return d.resolveExploreModel(parent)
}

func (d *Daemon) resolveExploreModel(parent *scheduler.ExecutionRun) string {
	parentModel := ""
	if parent != nil {
		parentModel = firstNonEmpty(strings.TrimSpace(parent.EffectiveModel), strings.TrimSpace(parent.Model))
	}
	var catalog provider.Catalog
	var disabled map[string]bool
	if d != nil {
		catalog = d.providerCatalog
		disabled = d.disabledProviders
	}
	if cheaper := cheapestSameProviderModel(catalog, parentModel, disabled); cheaper != "" {
		return cheaper
	}
	return parentModel
}

func cheapestSameProviderModel(catalog provider.Catalog, parentModel string, disabled map[string]bool) string {
	providerID, short := splitCatalogModelID(parentModel)
	if providerID == "" || short == "" || disabled[providerID] {
		return ""
	}
	info, ok := catalog[providerID]
	if !ok || len(info.Models) == 0 {
		return ""
	}
	parent, ok := lookupCatalogModel(info.Models, short)
	if !ok || parent.Cost == nil {
		return ""
	}
	parentCost := parent.Cost.Input + parent.Cost.Output
	bestID := ""
	bestCost := parentCost
	for id, model := range info.Models {
		if model.Cost == nil || strings.EqualFold(model.Status, "deprecated") {
			continue
		}
		candidate := strings.TrimSpace(id)
		if candidate == "" {
			candidate = strings.TrimSpace(model.ID)
		}
		if candidate == "" || candidate == short {
			continue
		}
		cost := model.Cost.Input + model.Cost.Output
		if cost >= parentCost {
			continue
		}
		if bestID == "" || cost < bestCost || (cost == bestCost && candidate < bestID) {
			bestCost = cost
			bestID = candidate
		}
	}
	if bestID == "" {
		return ""
	}
	return providerID + "/" + bestID
}

func lookupCatalogModel(models map[string]provider.Model, short string) (provider.Model, bool) {
	if model, ok := models[short]; ok {
		return model, true
	}
	for id, model := range models {
		if id == short || model.ID == short {
			return model, true
		}
	}
	return provider.Model{}, false
}

func (d *Daemon) composeSubagentPromptLayers(sess *sessionstore.Session, task *scheduler.ExecutionRun, spec *AgentSpec, memorySnapshot string) promptLayers {
	if isExploreSubagent(spec) {
		return d.composeExplorePromptLayers(sess, task, spec)
	}
	layers := promptLayers{Constitution: spec.SystemPrompt + "\n\n" + toolsHelp}
	if strings.TrimSpace(memorySnapshot) != "" {
		layers.Workspace = "CARINA PERSISTENT MEMORY SNAPSHOT (frozen for this run; background reference, not new user input):\n" + memorySnapshot
	}
	return layers
}

func (d *Daemon) composeExplorePromptLayers(sess *sessionstore.Session, task *scheduler.ExecutionRun, spec *AgentSpec) promptLayers {
	constitution := strings.TrimSpace(spec.SystemPrompt)
	if constitution == "" {
		constitution = builtinAgentSpecs()["explore"].SystemPrompt
	}
	constitution += "\n\n" + exploreToolsHelp

	sandboxState := "disabled"
	if d != nil && d.sandbox.Load() {
		sandboxState = "enabled"
	}
	var workspace strings.Builder
	if task != nil {
		if language := outputLanguagePrompt(task.Locale); language != "" {
			workspace.WriteString(language)
			workspace.WriteString("\n\n")
		}
	}
	root := ""
	if sess != nil {
		root = sess.WorkspaceRoot
	}
	fmt.Fprintf(&workspace, "RUNTIME SCOPE (authoritative): workspace_root=%q; os_sandbox=%s. Explore through read-only tools. Do not edit, run commands, or load project instruction files.", root, sandboxState)
	return promptLayers{
		Constitution: constitution,
		Workspace:    strings.TrimSpace(workspace.String()),
	}
}
