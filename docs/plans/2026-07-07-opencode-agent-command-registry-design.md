# OpenCode Agent And Command Registry Absorption

## Context

Carina already has isolated subagents, plan mode, prompt compaction, provider
catalogs, and BYOK runtime adapters. OpenCode's next useful pattern is not a new
language stack; it is the product surface around agent modes and slash commands:
agents are discoverable modes with prompts, permissions, and default models, and
commands are reusable prompt templates from built-ins, user config, project
config, MCP prompts, and skills.

Carina should absorb the local, auditable part first. ACP compatibility and
message-level timeline revert are valuable, but each introduces a wider protocol
or snapshot boundary and should be reviewed separately.

## Design

1. Extend `AgentSpec` into a first-class registry:
   - built-ins: `build`, `plan`, `general`, `explore`;
   - user agents from `~/.carina/agents/*.md`;
   - project agents from `<workspace>/.carina/agents/*.md`;
   - project definitions override user definitions, and custom definitions
     override built-ins by name.
2. Add `agent.list` and `carina agents list` so users can see available modes.
3. Let tasks carry `agent` in addition to `model`.
4. Main agent runs with the selected agent prompt. The `plan` agent also enables
   existing plan mode, so edits and commands remain blocked until approval.
5. Add a command registry:
   - built-ins: `/review`, `/init`;
   - user commands from `~/.carina/commands/*.md`;
   - project commands from `<workspace>/.carina/commands/*.md`;
   - `$ARGUMENTS` and `$1`, `$2`, ... placeholders are expanded.
6. Add `command.list`, `carina commands list`, and slash-command expansion in
   `task.submit`.

## Non-Goals

- No dynamic code loading.
- No MCP prompt import in this pass; Carina's MCP integration stays tool-focused.
- No ACP server implementation in this pass.
- No message-level filesystem revert in this pass.

## Tests

Unit tests cover built-in/custom agent loading, agent override precedence,
command template expansion, slash-command task submission, and CLI argument
parsing. Existing daemon and router tests continue to cover execution safety and
model selection.
