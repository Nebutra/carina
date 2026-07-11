package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// AgentSpec defines a subagent: an isolated, capability-restricted persona
// the main agent can delegate to. Loaded from markdown + frontmatter, in the
// same style as other coding-agent CLIs. The `profile` field is the capability ceiling
// — enforced by the Rust kernel, and further attenuated so a child can never
// exceed its parent (child ⊆ parent).
type AgentSpec struct {
	Name         string
	Description  string
	Profile      string // capability ceiling: read-only | safe-edit | ci-runner | full-workspace | ...
	Model        string
	Mode         string // primary | subagent
	Hidden       bool
	MaxTurns     int
	SystemPrompt string
	Source       string // "built-in" | "user" | "project"

	// RestrictedTools names tool verbs this agent's loop must never dispatch,
	// enforced in dispatchActionOutcome before the tool switch (belt-and-
	// suspenders on top of Profile: Profile denies an effect at the kernel
	// capability gate, but some kernel calls create real governed state before
	// any gate check exists (e.g. kernel.patch.propose has no capability gate
	// ahead of kernel.patch.apply — see crates/carina-kernel/src/bin/
	// carina-kernel-service.rs patch_propose). A restricted tool is refused
	// before it ever reaches the dispatch switch, so it can never create that
	// state at all, regardless of profile.
	RestrictedTools map[string]bool
}

type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Profile     string `json:"profile,omitempty"`
	Model       string `json:"model,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Source      string `json:"source,omitempty"`
}

// profileRank orders the built-in profiles by how permissive they are, so a
// subagent's requested profile can be clamped to never exceed its parent.
var profileRank = map[string]int{
	"read-only":             0,
	"sandboxed":             0,
	"enterprise-restricted": 1,
	"safe-edit":             2,
	"ci-runner":             2,
	"full-workspace":        3,
	"trusted-local":         4,
}

// attenuate returns the more-restrictive of the parent and requested profile
// (capability monotonic decrease — the core subagent safety invariant).
func attenuate(parent, requested string) string {
	if requested == "" {
		return "read-only" // default: least privilege
	}
	pr, ok1 := profileRank[requested]
	pp, ok2 := profileRank[parent]
	if !ok1 || !ok2 {
		return "read-only"
	}
	if pr <= pp {
		return requested
	}
	return parent // child cannot exceed parent
}

// loadAgentSpecs discovers agents from the user dir (~/.carina/agents) and the
// project dir (<workspace>/.carina/agents). Project agents override user ones.
func loadAgentSpecs(workspaceRoot string) map[string]*AgentSpec {
	out := builtinAgentSpecs()
	if home, err := os.UserHomeDir(); err == nil {
		loadAgentsFromDir(filepath.Join(home, ".carina", "agents"), "user", out)
	}
	if workspaceRoot != "" {
		loadAgentsFromDir(filepath.Join(workspaceRoot, ".carina", "agents"), "project", out)
	}
	return out
}

func builtinAgentSpecs() map[string]*AgentSpec {
	return map[string]*AgentSpec{
		"build": {
			Name:         "build",
			Description:  "Default coding agent for implementation work.",
			Profile:      "safe-edit",
			Mode:         "primary",
			MaxTurns:     8,
			Source:       "built-in",
			SystemPrompt: "You are in build mode. Inspect the workspace, make targeted changes, run relevant checks, and finish with a concise engineering summary.",
		},
		"plan": {
			Name:         "plan",
			Description:  "Read-only planning agent for analysis before edits.",
			Profile:      "read-only",
			Mode:         "primary",
			MaxTurns:     8,
			Source:       "built-in",
			SystemPrompt: "You are in plan mode. Explore and reason, but do not edit files or run commands. Produce a concrete plan and wait for approval before implementation.",
		},
		"general": {
			Name:         "general",
			Description:  "General-purpose subagent for bounded research and multi-step work.",
			Profile:      "read-only",
			Mode:         "subagent",
			MaxTurns:     8,
			Source:       "built-in",
			SystemPrompt: "You are a general-purpose subagent. Complete the delegated task independently and return only the result summary.",
		},
		"explore": {
			Name:         "explore",
			Description:  "Fast read-only subagent for finding files, symbols, and code paths.",
			Profile:      "read-only",
			Mode:         "subagent",
			MaxTurns:     6,
			Source:       "built-in",
			SystemPrompt: "You are a codebase exploration specialist. Use list, search, and read. Do not edit files or run commands. Return exact paths and findings.",
		},
		"candidate-drafter": {
			Name:            "candidate-drafter",
			Description:     "Hidden read-only drafter used by best_of_n to produce a proposed diff without ever applying it.",
			Profile:         "read-only",
			Mode:            "subagent",
			Hidden:          true,
			MaxTurns:        8,
			Source:          "built-in",
			RestrictedTools: map[string]bool{"patch": true, "run": true, "memory": true, "spawn": true, "workflow": true, "mcp": true},
			SystemPrompt: `You are a candidate-drafter for Carina's best-of-n patch generation. You
explore the workspace (list/read/search/code.*) and design a full-file edit,
but you NEVER apply it yourself — the "patch" tool is unavailable to you and
will be denied if you try. Instead, finish with "done" whose "summary" field
is STRICT JSON (no markdown fences, no prose) with this exact shape:

{"files":[{"path":"rel/path","new_content":"FULL new file content"}],"rationale":"why this change satisfies the task"}

Include the complete new content for every file you touch. If you cannot
produce a valid candidate, still finish with "done" and an empty "files"
array plus a "rationale" explaining why.`,
		},
	}
}

func sortedAgentInfos(specs map[string]*AgentSpec, includeHidden bool) []AgentInfo {
	out := make([]AgentInfo, 0, len(specs))
	for _, spec := range specs {
		if spec == nil || spec.Name == "" || (spec.Hidden && !includeHidden) {
			continue
		}
		out = append(out, AgentInfo{
			Name:        spec.Name,
			Description: spec.Description,
			Profile:     spec.Profile,
			Model:       spec.Model,
			Mode:        spec.Mode,
			Source:      spec.Source,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func loadAgentsFromDir(dir, source string, out map[string]*AgentSpec) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if spec := parseAgentSpec(string(raw)); spec != nil && spec.Name != "" {
			spec.Source = source
			out[spec.Name] = spec
		}
	}
}

// parseAgentSpec parses `---`-delimited frontmatter + a markdown body.
func parseAgentSpec(content string) *AgentSpec {
	content = strings.TrimLeft(content, " \t\r\n")
	if !strings.HasPrefix(content, "---") {
		return nil
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil
	}
	front := rest[:end]
	body := rest[end+4:]
	body = strings.TrimLeft(body, "\r\n")

	spec := &AgentSpec{Profile: "read-only", MaxTurns: 8, SystemPrompt: body}
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		switch key {
		case "name":
			spec.Name = val
		case "description":
			spec.Description = val
		case "profile":
			spec.Profile = val
		case "model":
			spec.Model = val
		case "mode":
			spec.Mode = val
		case "hidden":
			spec.Hidden = val == "true" || val == "yes" || val == "1"
		case "max_turns":
			if n, err := strconv.Atoi(val); err == nil {
				spec.MaxTurns = n
			}
		}
	}
	if spec.Mode == "" {
		spec.Mode = "subagent"
	}
	return spec
}
