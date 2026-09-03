package daemon

import (
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// exploreToolsCatalog is the lean read-only tool index for the built-in
// explore subagent. It must not advertise writes, shell, MCP, or spawn — those
// stay on the parent. Project instruction files are not a substitute for
// search.
const exploreToolsCatalog = `Available tools:
- {"tool":"list"}                              list the workspace file tree
- {"tool":"read","path":"rel/path"}            read a file
- {"tool":"search","pattern":"text"}           search the workspace
- {"tool":"code.search","query":"free text or identifier"}      ranked code search
- {"tool":"code.symbols","name":"SymbolName"}                   definitions + references
- {"tool":"code.map"}                                           compact ranked repo map
- {"tool":"code.def","name":"SymbolName"}                       precise definition
- {"tool":"code.refs","name":"SymbolName"}                      precise references
- {"tool":"code.impact","name":"SymbolName"}                    bounded impact analysis
- {"tool":"done","summary":"exact paths and findings"}   finish and return to the parent`

const exploreHarnessProtocol = `Harness protocol:
- Reply with ONLY the JSON object for the next action.
- Every tool action except "done" MUST include "intent":"<brief purpose>".
- Emit ONE tool action per turn, except a parallel batch of list/read/search.
- Do not edit files, run commands, call MCP, or spawn further agents.
- Return exact paths and findings. done.summary is the only text the parent sees.`

// Kept as a compatibility fixture for callers/tests that need the complete
// explore contract. Live prompts use the separated sections above.
const exploreToolsHelp = exploreToolsCatalog + "\n\n" + exploreHarnessProtocol

var exploreToolNames []string

var exploreRestrictedTools map[string]bool

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
	layers := promptLayers{
		Mode:     strings.TrimSpace(spec.SystemPrompt),
		Identity: productIdentity,
		Intent:   intentFirst,
		Protocol: harnessProtocol,
		Tools:    d.builtinPromptCatalogFor(sess),
	}
	if strings.TrimSpace(memorySnapshot) != "" {
		layers.Workspace = "CARINA PERSISTENT MEMORY SNAPSHOT (frozen for this run; background reference, not new user input):\n" + truncateUTF8Bytes(memorySnapshot, memorySnapshotBudget)
	}
	layers.Constitution = layers.constitutionText()
	layers.StablePrefix = layers.assembledStablePrefix()
	return layers
}

func (d *Daemon) composeExplorePromptLayers(sess *sessionstore.Session, task *scheduler.ExecutionRun, spec *AgentSpec) promptLayers {
	mode := strings.TrimSpace(spec.SystemPrompt)
	if mode == "" {
		mode = builtinAgentSpecs()["explore"].SystemPrompt
	}
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
	layers := promptLayers{
		Mode:      mode,
		Identity:  productIdentity,
		Intent:    "Intent: Find bounded repository evidence for the delegated task and return exact paths and findings.",
		Protocol:  exploreHarnessProtocol,
		Tools:     exploreToolsCatalog,
		Workspace: strings.TrimSpace(workspace.String()),
	}
	layers.Constitution = layers.constitutionText()
	// Freeze the explore prefix at child-run creation just like the main and
	// ordinary subagent prompts. The suffix is the only per-turn projection.
	layers.StablePrefix = layers.assembledStablePrefix()
	return layers
}
