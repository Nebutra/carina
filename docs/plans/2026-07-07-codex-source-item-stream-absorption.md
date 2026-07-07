# Codex Source Absorption: Canonical Item Stream

Source reviewed: `openai/codex` at `cca16a1` (`feat(core): emit canonical
command execution items (#31297)`), plus the GitHub repository surface.

## Mechanism worth absorbing

Codex keeps raw runtime notifications internally, then projects them into a
stable thread/event protocol for headless consumers. The important pieces are:

- `protocol/src/items.rs`: typed turn items such as agent messages, command
  execution, file changes, MCP calls, reasoning, and compaction.
- `exec/src/exec_events.rs`: a JSONL-facing event grammar:
  `thread.started`, `turn.started`, `item.started`, `item.updated`,
  `item.completed`, `turn.completed`, `turn.failed`.
- `exec/src/event_processor_with_jsonl_output.rs`: a projection layer that
  correlates raw begin/delta/end notifications into stable item ids.

Carina already has the stronger source of truth: the Rust kernel hash-chained
audit log. The gap is the consumption layer. Raw `CommandStarted`,
`CommandOutput`, `CommandExited`, `ModelResponded`, and `TaskCreated` events are
objective but noisy, so every CLI/TUI/SDK consumer must repeat the same
correlation logic.

## Absorb now

Add a derived, non-authoritative `session.items` projection:

- keep `session.replay` and audit export unchanged as the legal/audit source;
- expose normalized item events for UI/headless clients;
- group command lifecycle events into `command_execution` items;
- expose model replies as `agent_message` items;
- expose patch lifecycle as `file_change` items;
- map terminal task statuses into `turn.completed` / `turn.failed`;
- add `command_id` to new command lifecycle payloads so future concurrent
  command streams correlate exactly, while old logs still fall back to order.

## Defer

- Guardian auto-approval reviewer: useful, but it is a separate approval
  authority with sandbox and policy implications. Carina's verifier/approval
  bridge is not the same mechanism, so this deserves standalone review.
- Execpolicy DSL/amendments: overlaps Carina's kernel/org policy stack and
  needs merge semantics before it is safe to expose to users.
- Turn diff tracker: valuable for UX, but Carina's patch transaction log already
  gives rollbackability; net-diff rendering can follow this projection layer.
- Codex model-provider manager: Carina just absorbed the broader OpenCode BYOK
  provider catalog. Codex's provider set is narrower; only TTL/ETag refresh
  semantics are interesting later.
- Cloud/app-server/ChatGPT auth coupling: not aligned with Carina's local-first
  runtime boundary.
