# Claude Code → Carina — Feature Gap & Absorption Analysis

> Evidence status: historical backlog seed. This document does not pin a Claude
> Code source revision or a Carina baseline commit. Comparative and absolute
> language such as "most runtimes lack", "unkillable", "only sound fix", and
> "single biggest" is withdrawn. Re-audit each item against fixed source before
> using it as an implementation or product decision.

## Executive Summary

At the time of this analysis, Carina implemented a capability kernel with 7 permission profiles and risk classification, a tamper-evident hash-chained audit log, a transactional patch engine, a handle-only secret broker, signed WASM plugins, kernel-enforced sub-agent attenuation (child ⊆ parent), a declarative workflow DAG engine, durable background runs with per-turn checkpoint/resume, and a ReAct loop with compaction, loop-guard, retry, and graceful degrade. Current status must be checked from source before relying on this list.

The gaps concentrate in five themes, in rough priority order:

1. **Long-horizon survivability & economics** — multi-tier compaction, cost/token metering + budget gates, prompt-cache architecture, per-subagent budgets. These were proposed to improve cost and failure tolerance for durable/background runs.
2. **Execution soundness** — shell AST decomposition + per-subcommand gating, OS-level syscall sandbox, egress proxy, read-before-write stale-read guard, flag-level whitelists, path canonicalization. These close capability-gate bypasses on Carina's highest-risk surface (`run`).
3. **The remote/distributed roadmap** — a poll-for-work dispatch bridge (the missing half of the worker registry), resilient transport, remote/interactive permission bridge, attach/tail with replay cursor, a direct-connect HTTP/WS session API.
4. **Extensibility & interop** — MCP client + server mode, a skills/slash-command system, hooks, output styles, plugin bundles, hot-reload.
5. **Correctness plumbing** — tool-error-as-result, write-ahead-log ordering, double-buffered snapshots, post-edit diagnostics, structured output, memory/CLAUDE.md loading.

Many high-value items are **S/M effort** because they map cleanly onto primitives Carina already has (attenuation, audit chain, secret broker, event bus, session store, checkpoints).

## Already Absorbed (do not rebuild)

- Capability kernel: policy engine, 7 permission profiles, risk classification 0-5, every side effect capability-gated
- Tamper-evident hash-chained append-only audit log (`carina audit verify`)
- Transactional patch engine (propose/preview/apply/rollback, atomic, Zig-delegated writes)
- Secret broker: handle-only, redaction, `.env`/`.ssh`/`.aws` denylist
- WASM plugin runtime (wasmi, manifest+policy double-gate, ed25519-signed)
- Org policy bundle (tighten-only) + RBAC approval
- Sub-agent: markdown+frontmatter AgentSpec, isolated attenuated session, child ⊆ parent, single + parallel spawn, depth bound
- Workflow engine: declarative JSON DAG, parallel-where-deps-allow, `${step_id}`/`${input}` interpolation, resumable per-step
- Background runs: durable registry (survives restart), per-turn checkpoint + resume, concurrency semaphore, panic isolation
- ReAct loop: typed Transcript + compaction (elide+summarize, char budget) + loop-guard + retry (exp backoff) + graceful degrade
- GOAL mechanism: approval modes {untrusted, on_request, never}, success_criteria, approve-for-session memory
- Control plane: JSON-RPC 2.0 (unix socket + TCP), FIFO scheduler, session store + crash recovery, model router, event bus with live streaming
- Worker registry: register/heartbeat/list (substrate exists; workers do not yet execute)
- Native Zig tools with structured JSON: scan, grep, diff, patch-native, run, pty
- Agent tool set: list, read, search, run, patch, spawn, workflow, done

## Ranked Gap Table

| # | Gap | Chapters | Status | Value | Effort |
|---|-----|----------|--------|-------|--------|
| 1 | Multi-tier compaction (token threshold + circuit breaker + verbatim-user + rebuild) | 00,04,06,09 | partial | high | M |
| 2 | Cost & token metering + budget pause-and-approve | 01,03,06,07 | missing | high | M |
| 3 | Hooks lifecycle interception (PreToolUse/PostToolUse, `if`, exit-2) | 05,06 | missing | high | M |
| 4 | Shell AST decomposition + per-subcommand gating | 02 | missing | high | M |
| 5 | Distributed work-dispatch bridge (poll-for-work + lease + attenuated creds) | 08,00,01 | partial | high | L |
| 6 | Segmented prompt-cache (static/dynamic boundary + sticky latches + fork prefix) | 01,03,04,06 | missing | high | M |
| 7 | OS-level syscall sandbox (seccomp/namespaces, sandbox-exec) | 02 | partial | high | L |
| 8 | Egress proxy (gated network + boundary credential injection + NO_PROXY) | 08 | missing | high | L |
| 9 | Read-before-write stale/dirty-write guard (readFileState) | 02 | partial | high | M |
| 10 | MCP interop: client + server mode | 01 | missing | high | L |
| 11 | Schema-validated structured output for headless runs | 02 | missing | high | S |
| 12 | Workspace-trust gate (exact git-root keyed) before any execution | 01,08 | partial | high | M |
| 13 | Async steering of running subagent (SendMessage + pending queue + mailbox) | 04 | missing | high | M |
| 14 | Per-subagent task budget + resource caps | 04 | missing | high | S |
| 15 | Interactive + remote permission request/resolve protocol | 07,08,09 | partial | high | M |
| 16 | Leader permission bridge (bounded child→parent escalation) | 04 | partial | high | M |
| 17 | Post-edit diagnostics-delta loop + LSP intelligence | 02,06 | missing | high | M |
| 18 | Session fork-with-lineage + rewind-to-checkpoint | 03 | partial | high | M |
| 19 | Coordinator restricted-orchestrator + independent async verifier | 04 | partial | high | M |
| 20 | Skills / slash-command system (allowed-tools + inline vs fork) | 03,05 | partial | high | M |
| 21 | Persistent memory + hierarchical CLAUDE.md loading | 06,08 | missing | high | M |
| 22 | Tool-result disk offload + tail-truncation + pagination signal | 02 | partial | high | M |
| 23 | Tool-error-as-result contract (call() never throws) | 09 | partial | high | S |
| 24 | Write-ahead-log ordering (persist user turn before loop) | 09 | partial | high | S |
| 25 | Double-buffered message snapshot for concurrent submits | 00,09 | partial | high | S |
| 26 | Output styles (policy-governed system-prompt composition) | 07 | missing | high | S |
| 27 | Versioned idempotent config/state migration | 01 | missing | high | S |
| 28 | Atomic-write-safe config/spec hot-reload (validated staging swap) | 05 | missing | high | M |
| 29 | Per-session setting-source allowlist + MCP-layer filtering | 01 | partial | high | M |
| 30 | Flag-level read-only whitelist + injection denylist + path canonicalization + device/UNC guards | 02 | partial | high | M |
| 31 | Resilient model routing (529 fallback, source-aware retry, overflow shrink, heartbeat) | 06 | partial | medium | M |
| 32 | Intra-turn parallel tool execution (fail-closed partition) + streaming exec | 00,02,04,09 | partial | medium | M |
| 33 | Anti-tamper hardening (anti-debug, prctl DUMPABLE=0, commit-then-cleanup) | 01,08 | missing | medium | S |
| 34 | Attach/tail with replay cursor + reconnect dedup | 07,08 | partial | medium | M |
| 35 | Deferred tool-schema + health-gated pool assembly + ToolSearchTool | 00,02 | missing | medium | M |
| 36 | Ordered auth chain + managed-context isolation + lockfile refresh + apiKeyHelper | 00,01,06 | partial | medium | M |
| 37 | buildTool() middleware seam (auto gate+audit+metrics on every tool) | 00 | partial | medium | S |
| 38 | Duplicate-key JSON detection before parse (fail-closed) | 05 | missing | medium | S |
| 39 | Doctor / system-health surface (independent async probes) | 07 | missing | medium | S |
| 40 | Model tiering (cheap model for compaction/classification) | 09 | partial | medium | S |
| 41 | Plan mode (propose plan → approve → execute batch gate) | 03 | partial | medium | S |
| 42 | Central side-effect registry + worker-reconnect rehydration | 06 | partial | medium | M |
| 43 | Worktree canonical-root resolution + path-injection defense | 06,08 | partial | medium | M |
| 44 | Transport-origin command/tool allowlist + kill-switch + lazy OS-perm acquisition | 00,03,07 | partial | medium | M |
| 45 | Scoped runtime capability grant (/add-dir) + precedence-with-warnings | 03 | partial | medium | M |
| 46 | Ephemeral non-polluting side query with cache-safe reuse (/btw) | 03 | partial | medium | M |
| 47 | Plugin bundles + git marketplace + source allow/block-list + isAvailable | 05 | partial | medium | M |
| 48 | Content-block (image) + context-aware dynamic skill prompts | 05 | partial | medium | M |
| 49 | Task-notification loop-closing envelope (idempotent) | 04 | partial | medium | S |
| 50 | Direct-connect HTTP+WS session API (NDJSON, resume-via-index) | 08 | partial | medium | M |
| 51 | Cross-process history with chunked lazy load + singleflight | 07 | missing | low | S |

## Top Recommendations (absorb next, in order)

1. **Multi-tier compaction** — hardens durable/background/marathon runs against `prompt_too_long`, cuts always-heavy summarization cost, and adds a 3-strike circuit breaker so a failing summarizer never wedges the loop.
2. **Cost & token metering + budget pause-and-approve** — spend becomes a first-class safety control against runaway/prompt-injected agents, and the token count feeds the compaction trigger.
3. **Hooks lifecycle interception** — declarative, capability-gated seams around every tool (lint-on-write, secret-scan, model-driven verify), reusing the existing permission-rule predicate parser and exit-2 → Transcript injection.
4. **Shell AST decomposition + per-subcommand gating** — a proposed structural fix for command allowlisting on `run`; prefix matching is bypassable via `&& | ;` and subshells.
5. **Distributed work-dispatch bridge** — turns the existing worker registry into real remote executors with per-job attenuated secret-broker tokens, unblocking the whole remote/sandboxed roadmap.
6. **Segmented prompt-cache architecture** — a proposed cost/latency lever; built-in-first tool ordering plus a static/dynamic boundary may reduce cache churn on provider/MCP changes.
7. **Schema-validated structured output** — Carina is a JSON-RPC daemon; pipeline and workflow consumers need guaranteed machine-parseable, self-correcting final output.
8. **Read-before-write stale-read guard** — closes the one correctness hole in the otherwise-atomic patch engine: silent corruption when a concurrent agent/hook/formatter mutates a file edited from a stale base.

## Per-Chapter Appendix

- **00 — 全局架构总览:** Four-tier compaction ladder, normalize→validate→gate→execute tool lifecycle, intra-turn parallel tools, lazy tool-pool assembly, buildTool middleware seam, ordered auth chain + managed-context gate, `REMOTE_SAFE_COMMANDS`, double-buffered snapshot. Theme: pipeline discipline and per-request economy Carina should own at request-construction time.
- **01 — 核心入口:** MCP server mode, resilient reconnecting transport (CCR), prompt-cache sticky latches, per-session setting-source allowlist, versioned migrations, workspace-trust gate, anti-debug, trust-gated metering. Theme: startup/boundary correctness and the security gates that must precede any execution.
- **02 — 工具系统:** Read-before-write optimistic lock, shell AST per-subcommand gating, OS syscall sandbox, tool-result disk offload, flag-level whitelist + injection denylist, per-tool concurrency-safety flag, structured output, deferred tool activation, LSP, pre-permission canonicalization. Theme: making the tool boundary sound and token-bounded — the densest cluster of high-value execution-soundness gaps.
- **03 — 命令系统:** Slash-command registry, prompt-command shell inlining + minimal allowlist, `/context` (what-model-sees), cost/plan metering, fork + rewind, `/btw` side query, remote-safe allowlist, `/add-dir` scoped grant, plan mode. Theme: the operator/agent control surface and non-linear session graph.
- **04 — Agent协调:** Async steering (SendMessage), intra-turn parallel partition, leader permission bridge, per-subagent budget, coordinator/verifier separation, graded compaction + breaker, task-notification envelope, cache-aware fork, built-in Explore/Plan/verify agents. Theme: multi-agent governance and steerability.
- **05 — 扩展系统:** Skills as governed prompt-workflows, inline vs fork context, hardened lazy asset extraction, hot-reload, plugin bundles + marketplace, duplicate-key detection, source allow/block-list, content-block prompts. Theme: a governed, hot-reloadable extension surface — fills the slash-command gap.
- **06 — 服务与基础:** Hooks, post-edit diagnostics delta, cost metering, segmented prompt-cache, memory/CLAUDE.md, four-layer compaction + rebuild + breaker, resilient routing, managed-auth isolation, central side-effect registry, worktree canonical-root. Theme: the daemon's foundational services.
- **07 — UI与交互:** Output styles, cost-threshold pause dialog, interactive permission protocol, Doctor health surface, progressive resume picker, remote kill-switch + lazy acquisition, delta-streaming attach with resync, cross-process history. Theme: interaction surfaces that double as governance controls.
- **08 — 网络与远程:** Work-dispatch bridge, egress proxy + credential injection, anti-ptrace prctl, direct-connect server, remote permission bridge, observe/attach with replay, trust-gated auto-activation. Theme: the remote-execution substrate, recorded here as a large architectural cluster on the historical roadmap.
- **09 — 实战指南/01-搭建轻颗粒Agent客户端:** Tool-error-as-result, WAL ordering, double-buffered snapshot, streaming tool execution, per-tool timeout-as-feedback, model tiering, mid-tier structural compression, Promise-based permission decoupling. Theme: the load-bearing correctness invariants for a concurrent, crash-safe agent daemon — several are small, high-value hardening wins.
