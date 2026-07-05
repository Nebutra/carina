package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AgentSpec defines a subagent: an isolated, capability-restricted persona
// the main agent can delegate to. Loaded from markdown + frontmatter, exactly
// like Claude Code / pi agents. The `profile` field is the capability ceiling
// — enforced by the Rust kernel, and further attenuated so a child can never
// exceed its parent (child ⊆ parent).
type AgentSpec struct {
	Name         string
	Description  string
	Profile      string // capability ceiling: read-only | safe-edit | ci-runner | full-workspace | ...
	Model        string
	MaxTurns     int
	SystemPrompt string
	Source       string // "user" | "project"
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
	out := map[string]*AgentSpec{}
	if home, err := os.UserHomeDir(); err == nil {
		loadAgentsFromDir(filepath.Join(home, ".carina", "agents"), "user", out)
	}
	if workspaceRoot != "" {
		loadAgentsFromDir(filepath.Join(workspaceRoot, ".carina", "agents"), "project", out)
	}
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
		case "max_turns":
			if n, err := strconv.Atoi(val); err == nil {
				spec.MaxTurns = n
			}
		}
	}
	return spec
}
