# Carina — Zero-Gap Absorption Plan

## Purpose

This document sequences the absorption of all 50 remaining Claude-Code-parity gaps
(`docs/research/claude-code-gap-analysis.md`) into Carina's Go control plane + Rust
capability kernel + Zig native tools. Every gap already has a codebase-grounded spec
(file:function + approach + tests + effort). This plan turns those specs into a
dependency-ordered, mostly-additive execution ladder: **45 of 50 gaps land now in tested
increments**; **5 are large subsystems shipped as pragmatic MVPs in the final wave.**

Guiding invariants (unchanged across every wave):
- **Fail-closed** at trust boundaries (unknown = deny/higher-risk).
- **Tighten-only** composition — new layers (styles, org policy, trust, config sources,
  budgets) may only remove capability, never add.
- **Kernel is the single choke point** — every side effect flows through `d.kern.Request`
  + hash-chained audit; new tools/hooks/MCP proxies never bypass it.
- **Additive-first** — new files + new seams; existing behavior stays green unless a spec
  explicitly hardens it.

## Wave Sequence

### Wave 1 — Loop & I/O correctness foundations
Dependency-free, high-leverage S/M items that every later wave leans on.
- **Shell compound-command decomposition hardening** — extend `shell_segments`
  (`crates/carina-policy/src/lib.rs`) with the missing split boundaries (`&` background,
  redirections, process substitution, command-carrying builtins) and extract the
  classifier to `command.rs`; keeps the fail-closed "over-split only raises risk" invariant.
- **Tool-error-as-result contract** — `executeAction` returns `(content, isErr)`; add
  `safeExecuteAction` per-call panic recovery so a tool crash becomes a recoverable
  observation instead of killing the run; `IsError` observations survive compaction.
- **Write-ahead-log ordering** — write a Turn:0 checkpoint in `runTask` before the first
  model call so a turn-1 crash resumes instead of dying.
- **Read-before-write invariant + optimistic lock** — a `readLedger` records read hashes;
  `agentPatch` refuses a patch to an unread or disk-changed file, plus an
  `ExpectedBaseHash` per-file guard in the kernel `patch_propose`.
- **Duplicate-key JSON detection** — `hasDuplicateKeys` at both trust boundaries
  (Go `parseAction`, Rust `handle_line`) rejects smuggled `{"tool":"read","tool":"run"}`.
- **Model tiering** — a second cheaper `Reasoner` (`PI_SUMMARIZER_MODEL`) for
  compaction/summarization, falling back to the primary when unset.

### Wave 2 — Execution-security gating, transport hardening & context bounding
- **Flag-level whitelist + injection denylist + path/device guards** — replace bare-prefix
  read-only matching with a per-command deny-flag table (`find -delete`, `git commit`,
  `sed -i`), a redirection/injection denylist, and a `path_guard.rs` rejecting device/proc/
  socket/UNC paths before the workspace check.
- **Transport-origin allowlist + kill-switch + lazy bind** — thread listener Origin through
  `go/rpc` dispatch; remote callers get a read/observe allowlist only, a local-only
  killswitch, and TCP bound only when `RemoteEnabled`.
- **Workspace-trust gate** — exact git-root-keyed trust store; untrusted workspaces get a
  mandatory `untrusted-workspace` deny-bundle (no exec/patch/spawn) until
  `workspace.trust.grant` (local-only).
- **Double-buffered message snapshot** — `Transcript` gains an RWMutex + `snapshot()`; a
  `runInbox` drained at each turn boundary — the load-bearing seam for async steering.
- **Tool-result disk offload** — offload oversized (incl. pinned) tool output to a
  `resultStore`, substitute a head+tail preview with a `read_result` pagination signal.
- **Multi-tier compaction** — token-threshold trigger + summarize circuit-breaker +
  verbatim-user preservation + a rebuild-with-key-files top tier so the loop can never wedge.

### Wave 3 — Cost governance, model resilience, durable state registry
- **Cost & token metering + budget gate** — a `Meter` fed by real CLI usage JSON; crossing
  a per-task/session ceiling pauses-and-approves (attended) or degrades (unattended),
  resumable via checkpoint.
- **Per-subagent budget + resource caps** — token/cost/wall-clock/fan-out caps on
  `AgentSpec`, transitive against the parent's remaining ceiling (whale protection);
  subagents degrade rather than pause.
- **Resilient model routing** — typed API errors, source-aware retry suppression,
  context-overflow `forceCompact` retry, 529 cross-model fallback, unattended heartbeat.
- **Central side-effect registry + worker-reconnect** — scheduler `onChange` hook centralizes
  task-state persistence + Bus publish; stable-ID `RegisterOrRehydrate` + durable
  `workerStore` so dropped workers reclaim their in-flight assignment.
- **Versioned config/state migration** — an idempotent, crash-safe `STATE_VERSION` ladder
  run before `sessionstore.Open`, prerequisite for schema evolution in later waves.
- **Cross-process command/prompt history** — day-chunked append-only JSONL, single daemon
  writer, `history.query` with lazy paged reads shared across all clients.

### Wave 4 — Agent surface + tool registry + structured output + prompt cache
- **buildTool() middleware seam** — collapse the hardcoded `executeAction` switch into a
  registry where every tool auto-composes validate → gate → handler → uniform audit →
  metrics. Foundational for MCP and the dynamic tool-pool.
- **Schema-validated structured output** — stdlib draft-07 subset validator + self-correcting
  `done.output` loop for headless runs.
- **Hooks lifecycle interception** — declarative Pre/Post/Stop hooks with a rule-predicate
  pre-filter; exit-2 blocks the tool and its stderr becomes feedback; hook commands run
  through the same kernel gate.
- **Skills / slash-command system** — governed prompt-workflows (`allowed-tools`, inline vs
  fork execution) built on a shared `parseFrontmatter` + `spawnSubagent`.
- **Plan mode** — read-only recon phase → `plan` tool → `waiting_approval` batch gate →
  resume-at-checkpoint on approve, reusing the existing approval machinery.
- **Output styles** — a single `composeSystemPrompt` seam layering [immutable security core]
  + [style] + [memory] + [tools]; styles are append-only and org-policy-pinnable.
- **Persistent memory + hierarchical CARINA.md** — git-root-upward CARINA.md loading with
  `@import` + budget truncation, plus a durable `memory` tool injected via the composer.
- **Segmented prompt-cache architecture** — split Static/Dynamic prompt segments with an
  `ephemeral` cache_control breakpoint and per-session sticky cache epoch.

### Wave 5 — Multi-agent coordination & advanced session lifecycle
- **Async steering** — durable per-session mailbox (MsgID-idempotent), drained as pinned
  system turns at each turn boundary; survives crash+resume.
- **Leader permission bridge** — subagent `requires_approval` re-runs against the PARENT
  session (child privilege = min(child, parent)), replacing the current unsafe self-approval.
- **Coordinator role + independent verifier** — a delegation-only `orchestrator` profile
  (first-class SpawnAgent capability) + success-criteria checks run in a fresh read-only
  sub-session (separation of duties).
- **Task-notification loop-closing** — typed idempotent `CompletionEnvelope` keyed on
  child+task, no-op on re-delivery; wraps existing string returns.
- **Intra-turn parallel tools** — a fail-closed concurrency partition runs a leading run of
  read-only actions in parallel, everything from the first mutating/same-path action serial.
- **Session fork-with-lineage + rewind** — peer fork copying truncated history + rewind that
  truncates the model view only (patches stay append-only).
- **Ephemeral side query (/btw)** — cache-safe out-of-band question reusing the loop's prompt
  prefix without polluting the transcript or checkpoint.
- **Attach/tail replay cursor** — per-session monotonic seq + bounded ring buffer +
  `ReplaySince`; reconnect resumes at `since=<seq>` with at-least-once dedup.

### Wave 6 — Config cascade, auth chain, permission UX & operational hardening
- **Per-session setting-source allowlist** — 4-layer (Managed/User/Project/Runtime) config
  with managed-locked keys and project-source filtering (untrusted repos can't inject specs).
- **Atomic hot-reload** — `atomic.Pointer` spec snapshots; invalid files keep the prior valid
  snapshot; poll+debounce watcher (dependency-free).
- **Ordered auth chain + managed isolation** — env→helper→file→managed source chain with
  lockfile-serialized refresh; managed creds redactable but never enumerable by the agent.
- **Scoped runtime grant (/add-dir)** — kernel `additional_roots` grant gated per-profile and
  org-policy-forbiddable; re-applied on recover; subagents don't inherit.
- **Interactive + remote permission protocol** — push `PermissionRequested` notifications,
  `permission.list`/`resolve`, TTL auto-deny, child→parent forwarding clamped to child ceiling.
- **Doctor/system-health surface** — concurrent per-probe-timeout health report (kernel ping,
  tools, reasoner, providers, state-dir, org-policy) that never blocks on one bad probe.
- **Anti-tamper process hardening** — PR_SET_DUMPABLE/PT_DENY_ATTACH + TracerPid check +
  RLIMIT_CORE 0 in both daemon and kernel; zeroize on SecretBroker drop.

### Wave 7 — Large subsystems (pragmatic MVPs) & dependents
- **MCP interop (client + server)** [L] — stdio client consuming external servers first, every
  proxied tool registered through the buildTool() gate; then HTTP transport + outbound
  server-mode exposing Carina's gated tools.
- **Deferred lazy tool-pool + ToolSearch** — compact tool index by default, health-gated
  assembly skipping dead MCP servers, a lexical `tool_search` returning full schemas on demand.
- **Direct-connect HTTP+WebSocket API** — thin net/http front delegating to existing
  session/task handlers + NDJSON/WS event stream with resume-via-index (reuses Wave 5 cursor).
- **Distributed work-dispatch bridge** [L] — scheduler leases + worker poll-execute-report,
  gated by a new RemoteExecute capability with per-job attenuated credentials.
- **Egress proxy** [L] — loopback CONNECT/HTTP proxy calling the kernel NetworkAccess verdict
  per host (deny-by-default) with boundary credential injection; pairs with the OS sandbox.
- **OS-level syscall sandbox** [L] — carina-run `--sandbox` mapped from profile: macOS
  SBPL/sandbox-exec, Linux unshare+NO_NEW_PRIVS+seccomp allowlist, env-jail fallback.
- **Post-edit diagnostics-delta + LSP** [L] — Stage 1 linter-delta after PatchApply (additive
  MVP), Stage 2 real LSP servers for precise deltas + definition/hover tools.
- **Composable plugin bundles + git marketplace** — bundle manifests + tri-level enable merge
  (enterprise tighten-only) + git-clone install with permission inspection.
- **Content-block images + dynamic skills** — image content blocks (Anthropic path; CLI
  degrades) + context-triggered skill injection composed into the Static prompt segment.

## Additive-vs-Subsystem Split

**Additive now (45):** everything except the five L-effort subsystems below. These land as
independent, test-first increments — new files + new seams, existing suites stay green. Waves
1–6 are entirely additive; Wave 7 additive dependents (tool-pool, HTTP API, bundles, dynamic
skills) ride on the subsystems but are themselves incremental.

**Large subsystems (5), shipped as pragmatic MVPs in Wave 7:**
1. MCP interop (stdio client first, gate-enforced).
2. Distributed work-dispatch bridge (lease + poll + attenuated creds).
3. Egress proxy (deny-by-default host verdict).
4. OS syscall sandbox (profile-mapped, graceful fallback).
5. Post-edit diagnostics + LSP (Stage-1 linter delta is itself additive; Stage-2 LSP is the
   subsystem).

## Cross-Cutting Dependency Notes
- The **kernel gate + audit** and **runStore atomic-write/checkpoint** patterns are reused by
  nearly every gap; no new persistence mechanism is introduced.
- `buildTool()` (Wave 4) is a hard prerequisite for MCP and the dynamic tool-pool (Wave 7).
- The **config-migration → setting-source allowlist → hot-reload/auth/add-dir** chain
  (Waves 3→6) must land in order; each later config gap reads the layered config the prior
  established.
- The **attach/tail replay cursor** (Wave 5) is the resume-via-index substrate for the HTTP/WS
  API (Wave 7).
- The **double-buffered snapshot** (Wave 2) is what async steering (Wave 5) drains into.


---

## Tracking checklist

## Carina Zero-Gap Absorption — Tracking Checklist

### Wave 1 — Loop & I/O correctness foundations
- [ ] Shell compound-command decomposition hardening — [S/additive]
- [ ] Tool-error-as-result contract (call never throws) — [S/additive]
- [ ] Write-ahead-log ordering (persist user turn before loop) — [S/additive]
- [ ] Read-before-write invariant + optimistic-lock stale/dirty-write detection — [M/additive]
- [ ] Duplicate-key JSON detection (fail-closed) — [S/additive]
- [ ] Model tiering (cheap model for compaction/summarization) — [S/additive]

### Wave 2 — Execution-security gating, transport hardening & context bounding
- [ ] Flag-level read-only whitelist + injection denylist + path/device guards — [M/additive]
- [ ] Transport-origin allowlist + remote kill-switch + lazy OS-permission acquisition — [M/additive]
- [ ] Workspace-trust gate (git-root keyed) — [M/additive]
- [ ] Double-buffered message snapshot for concurrent submits — [S/additive]
- [ ] Tool-result disk offload + reference substitution + pagination signal — [M/additive]
- [ ] Multi-tier compaction (token trigger + circuit breaker + verbatim-user + rebuild-with-key-files) — [M/additive]

### Wave 3 — Cost governance, model resilience, durable state registry
- [ ] Cost & token metering with budget governance (pause-and-approve gate) — [M/additive]
- [ ] Per-subagent task budget + resource caps (whale protection) — [S/additive]
- [ ] Resilient model routing (cross-model fallback + typed-error retry + overflow auto-shrink + heartbeat) — [M/additive]
- [ ] Central side-effect registry + worker-reconnect state rehydration — [M/additive]
- [ ] Versioned idempotent config/state migration — [S/additive]
- [ ] Cross-process command/prompt history with chunked lazy load — [S/additive]

### Wave 4 — Agent surface: hooks / skills / plan / styles / memory + tool registry + structured output + prompt cache
- [ ] buildTool() middleware seam (auto gate+audit+metrics) — [M/additive]
- [ ] Schema-validated structured output for headless runs — [S/additive]
- [ ] Hooks lifecycle interception (Pre/Post/Stop + exit-2 blocking) — [M/additive]
- [ ] Skills / slash-command system (governed prompt-workflows) — [M/additive]
- [ ] Plan mode (propose -> approve -> execute gate) — [S/additive]
- [ ] Output styles (layered policy-governed system-prompt composition) — [S/additive]
- [ ] Persistent memory subsystem + hierarchical CARINA.md loading — [M/additive]
- [ ] Segmented prompt-cache architecture — [M/additive]

### Wave 5 — Multi-agent coordination & advanced session lifecycle
- [ ] Async steering of a running agent (durable mailbox + turn-boundary drain) — [M/additive]
- [ ] Leader permission bridge (bounded child->parent escalation) — [M/additive]
- [ ] Coordinator restricted-orchestrator role + independent async verifier — [M/additive]
- [ ] Task-notification loop-closing protocol (idempotent completion envelope) — [S/additive]
- [ ] Intra-turn parallel tool execution with concurrency-safety partition — [M/additive]
- [ ] Session fork-with-lineage + rewind-to-checkpoint — [M/additive]
- [ ] Ephemeral non-polluting side query (/btw) — [S/additive]
- [ ] Attach/tail with replay cursor + reconnect dedup — [M/additive]

### Wave 6 — Config cascade, auth chain, permission UX & operational hardening
- [ ] Per-session setting-source allowlist + config/MCP-layer filtering — [M/additive]
- [ ] Atomic-write-safe config/spec hot-reload — [M/additive]
- [ ] Ordered multi-source auth chain + managed-context isolation + apiKeyHelper — [M/additive]
- [ ] Scoped runtime capability grant (/add-dir) + config precedence cascade — [M/additive]
- [ ] Interactive + remote permission request/resolve protocol — [M/additive]
- [ ] Doctor/system-health surface — [S/additive]
- [ ] Anti-tamper process hardening — [M/additive]

### Wave 7 — Large subsystems (pragmatic MVPs) & dependents
- [ ] MCP interop: client + server mode — [L/large-subsystem]
- [ ] Deferred lazy tool-schema + health-gated tool-pool + ToolSearch — [M/additive]
- [ ] Direct-connect HTTP+WebSocket session API — [M/additive]
- [ ] Distributed work-dispatch bridge (poll + lease + attenuated creds) — [L/large-subsystem]
- [ ] Egress proxy (network as gated capability + credential injection) — [L/large-subsystem]
- [ ] OS-level syscall sandbox (seccomp/namespaces, sandbox-exec/SBPL) — [L/large-subsystem]
- [ ] Post-edit diagnostics-delta feedback loop + LSP intelligence — [L/large-subsystem]
- [ ] Composable plugin bundles + git marketplace + tri-level enable merge — [M/additive]
- [ ] Content-block (image) + context-aware dynamic skill prompts — [M/additive]