package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type CommandSpec struct {
	Name        string
	Description string
	Agent       string
	Model       string
	Template    string
	Source      string // "built-in" | "user" | "project" | "mcp"
	Subtask     bool
	Hints       []string
	MCPServer   string
	MCPPrompt   string
	Arguments   []CommandArgument
}

type CommandArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type CommandInfo struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Agent       string            `json:"agent,omitempty"`
	Model       string            `json:"model,omitempty"`
	Source      string            `json:"source,omitempty"`
	Subtask     bool              `json:"subtask,omitempty"`
	Hints       []string          `json:"hints,omitempty"`
	Arguments   []CommandArgument `json:"arguments,omitempty"`
}

type ExpandedCommand struct {
	Name   string
	Prompt string
	Agent  string
	Model  string
}

func loadCommandSpecs(workspaceRoot string) map[string]*CommandSpec {
	out := builtinCommandSpecs()
	if home, err := os.UserHomeDir(); err == nil {
		loadCommandsFromDir(filepath.Join(home, ".carina", "commands"), "user", out)
	}
	if workspaceRoot != "" {
		loadCommandsFromDir(filepath.Join(workspaceRoot, ".carina", "commands"), "project", out)
	}
	return out
}

func builtinCommandSpecs() map[string]*CommandSpec {
	return map[string]*CommandSpec{
		"review": commandWithHints(&CommandSpec{
			Name:        "review",
			Description: "Review current changes or a named target.",
			Agent:       "explore",
			Source:      "built-in",
			Template: strings.TrimSpace(`Review the current workspace changes with a code-review stance.

Target or context: $ARGUMENTS

Prioritize bugs, behavioral regressions, security risks, and missing tests. Lead with findings and cite concrete files or symbols when possible.`),
		}),
		"init": commandWithHints(&CommandSpec{
			Name:        "init",
			Description: "Create or update project instructions for Carina.",
			Agent:       "build",
			Source:      "built-in",
			Template: strings.TrimSpace(`Inspect this repository and create or update CARINA.md with concise project-specific operating instructions.

Include build/test commands, important directories, coding conventions, and safety constraints. Preserve existing useful instructions.`),
		}),
	}
}

func loadCommandsFromDir(dir, source string, out map[string]*CommandSpec) {
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
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if spec := parseCommandSpec(name, string(raw)); spec != nil && spec.Name != "" {
			spec.Source = source
			out[spec.Name] = commandWithHints(spec)
		}
	}
}

func parseCommandSpec(fallbackName, content string) *CommandSpec {
	content = strings.TrimLeft(content, " \t\r\n")
	spec := &CommandSpec{Name: fallbackName, Template: content}
	if strings.HasPrefix(content, "---") {
		rest := content[3:]
		end := strings.Index(rest, "\n---")
		if end < 0 {
			return nil
		}
		front := rest[:end]
		spec.Template = strings.TrimLeft(rest[end+4:], "\r\n")
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
			case "agent":
				spec.Agent = val
			case "model":
				spec.Model = val
			case "subtask":
				spec.Subtask = val == "true" || val == "yes" || val == "1"
			}
		}
	}
	spec.Name = strings.TrimPrefix(strings.TrimSpace(spec.Name), "/")
	spec.Template = strings.TrimSpace(spec.Template)
	if spec.Name == "" || spec.Template == "" {
		return nil
	}
	return commandWithHints(spec)
}

func sortedCommandInfos(specs map[string]*CommandSpec) []CommandInfo {
	out := make([]CommandInfo, 0, len(specs))
	for _, spec := range specs {
		if spec == nil || spec.Name == "" {
			continue
		}
		out = append(out, CommandInfo{
			Name:        spec.Name,
			Description: spec.Description,
			Agent:       spec.Agent,
			Model:       spec.Model,
			Source:      spec.Source,
			Subtask:     spec.Subtask,
			Hints:       append([]string(nil), spec.Hints...),
			Arguments:   append([]CommandArgument(nil), spec.Arguments...),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func expandSlashCommand(input string, specs map[string]*CommandSpec) (*ExpandedCommand, bool, error) {
	name, args, ok, err := parseSlashCommand(input)
	if err != nil || !ok {
		return nil, ok, err
	}
	spec := specs[name]
	if spec == nil {
		return nil, true, fmt.Errorf("unknown command /%s", name)
	}
	if spec.MCPServer != "" || spec.MCPPrompt != "" {
		return nil, true, fmt.Errorf("command /%s requires daemon expansion", name)
	}
	return expandCommandSpec(name, args, spec), true, nil
}

func parseSlashCommand(input string) (name, args string, ok bool, err error) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return "", "", false, nil
	}
	head, rest, _ := strings.Cut(strings.TrimPrefix(trimmed, "/"), " ")
	name = strings.TrimSpace(head)
	if name == "" {
		return "", "", true, fmt.Errorf("command name required")
	}
	return name, strings.TrimSpace(rest), true, nil
}

func expandCommandSpec(name, args string, spec *CommandSpec) *ExpandedCommand {
	return &ExpandedCommand{
		Name:   name,
		Prompt: expandCommandTemplate(spec.Template, args),
		Agent:  spec.Agent,
		Model:  spec.Model,
	}
}

func expandCommandTemplate(template, args string) string {
	out := strings.ReplaceAll(template, "$ARGUMENTS", args)
	fields := strings.Fields(args)
	for i := len(fields); i >= 1; i-- {
		out = strings.ReplaceAll(out, "$"+strconv.Itoa(i), fields[i-1])
	}
	return strings.TrimSpace(out)
}

func commandWithHints(spec *CommandSpec) *CommandSpec {
	if spec == nil {
		return nil
	}
	spec.Hints = commandHints(spec.Template)
	return spec
}

func commandHints(template string) []string {
	seen := map[string]bool{}
	var hints []string
	add := func(v string) {
		if !seen[v] {
			hints = append(hints, v)
			seen[v] = true
		}
	}
	if strings.Contains(template, "$ARGUMENTS") {
		add("$ARGUMENTS")
	}
	for i := 1; i <= 9; i++ {
		key := "$" + strconv.Itoa(i)
		if strings.Contains(template, key) {
			add(key)
		}
	}
	return hints
}
