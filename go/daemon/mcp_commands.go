package daemon

import (
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/mcp"
)

func (d *Daemon) commandSpecs(workspaceRoot string) map[string]*CommandSpec {
	specs := loadCommandSpecs(workspaceRoot)
	if d == nil || d.mcp == nil {
		return specs
	}
	for _, prompt := range d.mcp.Prompts() {
		name := mcpPromptCommandName(prompt.Server, prompt.Name)
		if name == "" {
			continue
		}
		if _, exists := specs[name]; exists {
			continue
		}
		args := commandArgumentsFromMCP(prompt.Arguments)
		specs[name] = &CommandSpec{
			Name:        name,
			Description: prompt.Description,
			Source:      "mcp",
			Hints:       commandArgumentHints(args),
			MCPServer:   prompt.Server,
			MCPPrompt:   prompt.Name,
			Arguments:   args,
		}
	}
	return specs
}

func mcpPromptCommandName(server, prompt string) string {
	server = strings.TrimSpace(server)
	prompt = strings.TrimSpace(prompt)
	if server == "" || prompt == "" || strings.ContainsAny(server+prompt, " \t\r\n") {
		return ""
	}
	return "mcp." + server + "." + prompt
}

func commandArgumentsFromMCP(args []mcp.PromptArgument) []CommandArgument {
	out := make([]CommandArgument, 0, len(args))
	for _, arg := range args {
		name := strings.TrimSpace(arg.Name)
		if name == "" {
			continue
		}
		out = append(out, CommandArgument{
			Name:        name,
			Description: arg.Description,
			Required:    arg.Required,
		})
	}
	return out
}

func commandArgumentHints(args []CommandArgument) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, arg.Name)
	}
	return out
}

func (d *Daemon) expandTaskSlashCommand(input, workspaceRoot string) (*ExpandedCommand, bool, error) {
	specs := d.commandSpecs(workspaceRoot)
	name, args, ok, err := parseSlashCommand(input)
	if err != nil || !ok {
		return nil, ok, err
	}
	spec := specs[name]
	if spec == nil {
		return nil, true, fmt.Errorf("unknown command /%s", name)
	}
	if spec.MCPServer == "" && spec.MCPPrompt == "" {
		return expandCommandSpec(name, args, spec), true, nil
	}
	promptArgs, err := mcpPromptArgumentMap(name, args, spec.Arguments)
	if err != nil {
		return nil, true, err
	}
	prompt, err := d.mcp.GetPrompt(spec.MCPServer, spec.MCPPrompt, promptArgs)
	if err != nil {
		return nil, true, err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, true, fmt.Errorf("command /%s expanded to an empty prompt", name)
	}
	return &ExpandedCommand{Name: name, Prompt: prompt, Agent: spec.Agent, Model: spec.Model}, true, nil
}

func mcpPromptArgumentMap(commandName, tail string, defs []CommandArgument) (map[string]string, error) {
	tail = strings.TrimSpace(tail)
	if len(defs) == 0 {
		if tail != "" {
			return nil, fmt.Errorf("command /%s does not accept arguments", commandName)
		}
		return map[string]string{}, nil
	}
	out := map[string]string{}
	fields := strings.Fields(tail)
	pos := 0
	catchAll := false
	for _, def := range defs {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		val := ""
		switch {
		case strings.EqualFold(name, "ARGUMENTS"):
			val = tail
			catchAll = true
		case len(defs) == 1:
			val = tail
		case pos < len(fields):
			val = fields[pos]
			pos++
		}
		if val == "" {
			if def.Required {
				return nil, fmt.Errorf("command /%s requires argument %q", commandName, name)
			}
			continue
		}
		out[name] = val
	}
	if pos < len(fields) && !catchAll && len(defs) > 1 {
		return nil, fmt.Errorf("command /%s got too many arguments", commandName)
	}
	return out, nil
}
