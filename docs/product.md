# Product Positioning

Carina is a local-first runtime for AI coding agents. It is built for the point
where an agent leaves conversation and starts acting on a real repository.

## Primary Users

Carina is for:

- individual engineers who want agents to work on local repos with audit and
  rollback;
- platform teams building an internal agent runner;
- security-conscious teams evaluating how agents read files, run commands, and
  handle secrets;
- tool builders who need a runtime behind an IDE, TUI, CI workflow, or web UI.

It is not primarily for people who want a finished editor assistant or a hosted
managed agent service.

## User Value

Carina turns agent execution into a controlled runtime:

| Need | Carina answer |
|---|---|
| Let an agent act on a repo | Daemon sessions, task runner, model adapters |
| Control side effects | Capability kernel, profiles, approval modes, risk review |
| Explain what happened | Hash-chained audit log, normalized item stream, turn net diff |
| Recover from bad edits | Transactional patch apply and rollback |
| Use real models | BYOK credential chain and provider catalog |
| Embed in another product | JSON-RPC, scoped HTTP Gateway, MCP server/client, SDK surfaces |
| Split work safely | Sub-agent attenuation, workers, workflow DAGs |
| Coordinate endpoints later | Nebutra Cloud boundary for identity and sync, with Carina remaining local authority |

## Non-Goals

Carina should not pretend to be:

- a full editor product;
- a hosted cloud agent app;
- a complete VM/container isolation platform;
- a replacement for Git history or code review;
- a mature signed binary distribution while the project is still source-first.

## Objective Alternative Positioning

This section is intentionally not a feature attack table. Adjacent tools change
quickly, and each optimizes for a different job.

| Alternative category | Optimized for | Carina position |
|---|---|---|
| Editor assistants | Interactive editing inside an IDE | Carina can sit behind an editor, but it focuses on runtime governance. |
| CLI coding agents | Conversational terminal coding | Carina is less about chat UX and more about durable sessions, audit, rollback, workers, and embedding. |
| Cloud agent tasks | Managed remote execution and cloud UX | Carina is local-first; Nebutra Cloud identity/sync wraps it rather than living inside it. |
| Cloud sandboxes | Disposable isolated compute | Carina can use sandboxing, but its main object is a policy-gated repository action. |
| Custom internal stacks | Team-specific orchestration | Carina provides reusable runtime pieces instead of asking every team to rebuild policy/audit/patching. |

## Alpha Product Truth

Current strengths:

- control-plane and capability boundary exist;
- audit and patch provenance are implemented;
- provider/BYOK support is broad enough for practical testing;
- scoped HTTP Gateway exposes agent-first `/v1` and read-only tool invoke when
  explicitly enabled;
- source builds and tests are usable.
- Nebutra Cloud identity/sync has an explicit boundary contract, with sync off
  by default.

Current gaps:

- release packaging is source-first;
- Homebrew and npm install channels are planned but not published;
- dashboard/TUI is not polished;
- SDK parity is incomplete;
- Windows is not supported;
- production remote-worker operations need separate deployment documentation.
- the Nebutra Cloud sync connector is not implemented yet.
