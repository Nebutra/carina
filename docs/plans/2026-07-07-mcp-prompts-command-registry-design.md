# MCP Prompts Command Registry Design

## Context

OpenCode treats MCP prompts as part of its command surface instead of limiting
MCP to tools. Carina already has a slash command registry with built-in, user,
and project command templates, and it already connects to external MCP servers
for gated tool calls. The missing useful piece is to make MCP prompt templates
discoverable and executable from the same command workflow.

This pass intentionally does not absorb ACP session protocol support or
workspace revert checkpoints. ACP overlaps with Carina's existing JSON-RPC API
and CLI, and revert support requires a broader snapshot policy. MCP prompts are
smaller, user-facing, and fit the command registry directly.

## Scope

Add MCP prompt discovery and prompt rendering:

1. Extend the MCP client with `prompts/list` and `prompts/get`.
2. Cache prompt metadata per connected MCP server alongside tools.
3. Expose each prompt as `/mcp.<server>.<prompt>` in `command.list`.
4. Expand an MCP slash command by calling `prompts/get` and submitting the
   returned text as the task prompt.

MCP prompts are read-only prompt templates. Expanding one does not call MCP
tools, does not grant capabilities, and does not run code.

## Naming

All MCP prompts use deterministic names:

```text
/mcp.<server>.<prompt>
```

This avoids ambiguity with built-in, user, and project commands. It also avoids
the misleading behavior of exposing a bare prompt name that may later collide
when another server is added.

## Arguments

MCP prompt arguments come from the server's `prompts/list` metadata. Carina maps
the slash command tail into prompt arguments conservatively:

- one declared argument receives the full tail;
- multiple declared arguments receive whitespace-separated positional fields in
  declaration order;
- `ARGUMENTS` also receives the full tail for servers that choose to support a
  catch-all argument;
- empty optional arguments are omitted.

If a server rejects the arguments, task submission fails with the MCP error
rather than silently running a different prompt.

## Error Handling

Disconnected MCP servers reuse the existing reconnect-on-call path. Unknown MCP
commands return the same error shape as unknown local slash commands. A
`prompts/get` result with no text content is rejected as an empty command
expansion.

## Tests

Add a mock MCP server that supports both tools and prompts. Cover:

- prompt metadata discovery in `go/mcp`;
- `prompts/get` text flattening;
- command listing includes `/mcp.<server>.<prompt>`;
- `task.submit` expands an MCP prompt and preserves explicit model/agent
  overrides.
