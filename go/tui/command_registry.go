package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type commandDescriptor struct {
	Name, Usage, Description, Source string
	HelpID                           MessageID
	Validate                         func([]string) bool
}

func anyArgs([]string) bool     { return true }
func noArgs(a []string) bool    { return len(a) == 0 }
func zeroOrOne(a []string) bool { return len(a) <= 1 }

var builtinCommandRegistry = []commandDescriptor{
	{Name: "help", Usage: "/help", Description: "commands and keybindings", Source: "builtin", HelpID: MsgHelpCommandHelp, Validate: anyArgs},
	{Name: "keys", Usage: "/keys", Description: "commands and keybindings", Source: "builtin", Validate: noArgs},
	{Name: "editor", Usage: "/editor", Description: "edit draft in VISUAL/EDITOR", Source: "builtin", HelpID: MsgHelpCommandEditor, Validate: noArgs},
	{Name: "copy", Usage: "/copy", Description: "copy latest agent response", Source: "builtin", HelpID: MsgHelpCommandCopy, Validate: noArgs},
	{Name: "transcript", Usage: "/transcript", Description: "canonical session items", Source: "builtin", HelpID: MsgHelpCommandTranscript, Validate: noArgs},
	{Name: "keymap", Usage: "/keymap", Description: "inspect and edit keybindings", Source: "builtin", HelpID: MsgHelpCommandKeymap, Validate: noArgs},
	{Name: "agents", Usage: "/agents", Description: "available agent modes", Source: "builtin", HelpID: MsgHelpCommandAgents, Validate: noArgs},
	{Name: "checkpoints", Usage: "/checkpoints", Description: "checkpoint picker", Source: "builtin", HelpID: MsgHelpCommandCheckpoints, Validate: noArgs},
	{Name: "new", Usage: "/new", Description: "create and switch session", Source: "builtin", HelpID: MsgHelpCommandNew, Validate: noArgs},
	{Name: "clear", Usage: "/clear", Description: "start a new session without deleting history", Source: "builtin", Validate: noArgs},
	{Name: "rename", Usage: "/rename <name>", Description: "rename the current session", Source: "builtin", Validate: func(a []string) bool { return len(a) > 0 }},
	{Name: "resume", Usage: "/resume [session_id]", Description: "resume historical session", Source: "builtin", HelpID: MsgHelpCommandResume, Validate: zeroOrOne},
	{Name: "fork", Usage: "/fork [task_id]", Description: "fork session lineage", Source: "builtin", HelpID: MsgHelpCommandFork, Validate: zeroOrOne},
	{Name: "task-resume", Usage: "/task-resume [task_id]", Description: "resume paused task", Source: "builtin", HelpID: MsgHelpCommandTaskResume, Validate: zeroOrOne},
	{Name: "search", Usage: "/search <text>", Description: "search canonical session items", Source: "builtin", HelpID: MsgHelpCommandSearch, Validate: func(a []string) bool { return len(a) > 0 }},
	{Name: "recap", Usage: "/recap", Description: "latest canonical items", Source: "builtin", HelpID: MsgHelpCommandRecap, Validate: noArgs},
	{Name: "status", Usage: "/status", Description: "session status", Source: "builtin", HelpID: MsgHelpCommandStatus, Validate: noArgs},
	{Name: "permissions", Usage: "/permissions [new <safe-edit|full-workspace> [--yes]]", Description: "effective permissions or a governed new session", Source: "builtin", HelpID: MsgHelpCommandPermissions, Validate: func(a []string) bool {
		return len(a) == 0 || (len(a) == 2 && a[0] == "new" && a[1] == "safe-edit") || (len(a) == 3 && a[0] == "new" && a[1] == "full-workspace" && a[2] == "--yes")
	}},
	{Name: "context", Usage: "/context", Description: "persisted context summary", Source: "builtin", HelpID: MsgHelpCommandContext, Validate: noArgs},
	{Name: "compact", Usage: "/compact", Description: "atomically compact paused checkpoint", Source: "builtin", HelpID: MsgHelpCommandCompact, Validate: noArgs},
	{Name: "config", Usage: "/config [model|effort|mode|permissions|keymap|raw]", Description: "settings shell or raw inventory", Source: "builtin", HelpID: MsgHelpCommandConfig, Validate: anyArgs},
	{Name: "settings", Usage: "/settings", Description: "settings shell (alias of /config)", Source: "builtin", Validate: noArgs},
	{Name: "doctor", Usage: "/doctor", Description: "runtime diagnostics", Source: "builtin", HelpID: MsgHelpCommandDoctor, Validate: noArgs},
	{Name: "usage", Usage: "/usage", Description: "session token usage and cost", Source: "builtin", HelpID: MsgHelpCommandUsage, Validate: noArgs},
	{Name: "cost", Usage: "/cost", Description: "alias of /usage", Source: "builtin", Validate: noArgs},
	{Name: "review", Usage: "/review [target]", Description: "run a code review task", Source: "builtin", HelpID: MsgHelpCommandReview, Validate: anyArgs},
	{Name: "commit", Usage: "/commit", Description: "commit-oriented review/agent task", Source: "builtin", Validate: anyArgs},
	{Name: "btw", Usage: "/btw <question>", Description: "side question without replacing the main plan", Source: "builtin", Validate: func(a []string) bool { return len(a) > 0 }},
	{Name: "plan", Usage: "/plan [description]", Description: "enter plan mode, optionally with a planning prompt", Source: "builtin", Validate: anyArgs},
	{Name: "build", Usage: "/build", Description: "leave plan mode (build)", Source: "builtin", Validate: noArgs},
	{Name: "view-plan", Usage: "/view-plan", Description: "show plan mode status and approval guidance", Source: "builtin", Validate: noArgs},
	{Name: "tasks", Usage: "/tasks", Description: "active tasks and follow-up queue", Source: "builtin", Validate: noArgs},
	{Name: "ps", Usage: "/ps", Description: "alias of /tasks", Source: "builtin", Validate: noArgs},
	{Name: "sessions", Usage: "/sessions", Description: "resume session picker", Source: "builtin", Validate: noArgs},
	{Name: "export", Usage: "/export [path]", Description: "export plain transcript to a file", Source: "builtin", Validate: anyArgs},
	{Name: "remember", Usage: "/remember <note>", Description: "write a governed memory note", Source: "builtin", Validate: func(a []string) bool { return len(a) > 0 }},
	{Name: "init", Usage: "/init", Description: "create AGENTS.md project rules scaffold", Source: "builtin", Validate: noArgs},
	{Name: "compact-mode", Usage: "/compact-mode", Description: "toggle denser UI chrome", Source: "builtin", Validate: noArgs},
	{Name: "session-review", Usage: "/session-review", Description: "read-only governance projection", Source: "builtin", HelpID: MsgHelpCommandSessionReview, Validate: noArgs},
	{Name: "memory", Usage: "/memory [status|list|search|read|verify|handoff|rollback]", Description: "versioned persistent memory controller", Source: "builtin", HelpID: MsgHelpCommandMemory, Validate: func(a []string) bool {
		if len(a) == 0 || (len(a) == 1 && (a[0] == "status" || a[0] == "list")) || (len(a) >= 2 && a[0] == "search") {
			return true
		}
		return (len(a) == 1 || len(a) == 2) && a[0] == "read" ||
			(len(a) >= 1 && len(a) <= 3 && a[0] == "verify") ||
			(len(a) == 6 && a[0] == "rollback" && a[5] == "--yes") ||
			(len(a) == 6 && a[0] == "handoff" && a[5] == "--yes")
	}},
	{Name: "skills", Usage: "/skills", Description: "read-only skill inventory", Source: "builtin", HelpID: MsgHelpCommandSkills, Validate: noArgs},
	{Name: "hooks", Usage: "/hooks", Description: "read-only hook inventory", Source: "builtin", HelpID: MsgHelpCommandHooks, Validate: noArgs},
	{Name: "extensions", Usage: "/extensions", Description: "read-only extension inventory", Source: "builtin", HelpID: MsgHelpCommandExtensions, Validate: noArgs},
	{Name: "diff", Usage: "/diff", Description: "read-only workspace diff", Source: "builtin", HelpID: MsgHelpCommandDiff, Validate: noArgs},
	{Name: "mcp", Usage: "/mcp [verbose]", Description: "secret-free MCP inventory", Source: "builtin", HelpID: MsgHelpCommandMCP, Validate: func(a []string) bool { return len(a) == 0 || (len(a) == 1 && a[0] == "verbose") }},
	{Name: "loop", Usage: "/loop [list|<duration> [--concurrency policy] <prompt>|pause|resume|delete <id>]", Description: "scheduled tasks", Source: "builtin", HelpID: MsgHelpCommandLoop, Validate: anyArgs},
	{Name: "goal", Usage: "/goal [--auto] [--tokens N] [--max-continuations N] <objective>|clear|pause|resume|complete|continue", Description: "persistent session goal", Source: "builtin", HelpID: MsgHelpCommandGoal, Validate: anyArgs},
	{Name: "mode", Usage: "/mode <build|plan|cycle>", Description: "interaction mode", Source: "builtin", HelpID: MsgHelpCommandMode, Validate: func(a []string) bool {
		return len(a) == 1 && (a[0] == "build" || a[0] == "plan" || a[0] == "cycle")
	}},
	{Name: "model", Usage: "/model [provider/model]", Description: "task model picker", Source: "builtin", HelpID: MsgHelpCommandModel, Validate: zeroOrOne},
	{Name: "effort", Usage: "/effort [default|low|medium|high|max|auto]", Description: "reasoning effort", Source: "builtin", HelpID: MsgHelpCommandEffort, Validate: func(a []string) bool {
		if len(a) > 1 {
			return false
		}
		return len(a) == 0 || a[0] == "default" || a[0] == "low" || a[0] == "medium" || a[0] == "high" || a[0] == "max" || a[0] == "auto"
	}},
}

func builtinCommand(name string) (commandDescriptor, bool) {
	for _, d := range builtinCommandRegistry {
		if d.Name == name {
			return d, true
		}
	}
	return commandDescriptor{}, false
}
func builtinCommandNamesFromRegistry() []string {
	out := make([]string, 0, len(builtinCommandRegistry))
	for _, d := range builtinCommandRegistry {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return out
}
func validSlashCommand(text string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	d, ok := builtinCommand(strings.TrimPrefix(parts[0], "/"))
	return ok && d.Validate(parts[1:])
}

type dynamicSlashResolvedMsg struct {
	draft promptDraft
	found bool
	err   error
}

func (m *Model) resolveDynamicSlash(text string) tea.Cmd {
	return m.resolveDynamicSlashWithSkills(text)
}
func (m *Model) handleDynamicSlash(msg dynamicSlashResolvedMsg) tea.Cmd {
	if msg.err != nil {
		m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}))
		return nil
	}
	if !msg.found {
		m.push(m.text(MsgUpdateUnknownCommand, MessageArgs{"command": strings.TrimPrefix(strings.Fields(msg.draft.Text)[0], "/")}))
		return nil
	}
	return m.beginSubmissionSourceWithIntent(submissionTask, "", msg.draft, false, false)
}
