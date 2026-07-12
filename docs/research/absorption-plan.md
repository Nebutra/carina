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
- **Model tiering** — a second cheaper `Reasoner` (`CARINA_SUMMARIZER_MODEL`) for
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

This checklist was frozen unchecked for a long time while `absorption-status.md`
became the actual live tracker (waves 1–22+, plus the 2026-07-12 cline/codebuff
campaign). Reconciled 2026-07-12 against `absorption-status.md` and, for a
handful of ambiguous items, against current code directly (see inline notes).
Items are checked when the underlying gap is closed, even where the landed
shape or naming differs from this plan's original sketch — the plan predates
implementation and several gaps ended up satisfied by a broader or differently
named mechanism than first scoped. Treat `absorption-status.md` as authoritative
where the two disagree.

Convention for branch-landed work (adopted 2026-07-12 deep-tradeoff closeout,
stated once here): an item implemented on an isolated feature branch **stays
`[ ]` unchecked until that branch merges to main** — a checked box means the
gap is closed *on main*. Such items carry an "(implemented on branch …, pending
merge)" note naming the branch and short SHA. `already_covered` verdicts are
checked with the covering evidence cited.

## Carina Zero-Gap Absorption — Tracking Checklist

### Wave 1 — Loop & I/O correctness foundations
- [x] Shell compound-command decomposition hardening — [S/additive]
- [x] Tool-error-as-result contract (call never throws) — [S/additive] (confirmed already satisfied, `absorption-status.md` Wave 1)
- [x] Write-ahead-log ordering (persist user turn before loop) — [S/additive] (confirmed already satisfied, `absorption-status.md` Wave 1)
- [x] Read-before-write invariant + optimistic-lock stale/dirty-write detection — [M/additive]
- [x] Duplicate-key JSON detection (fail-closed) — [S/additive]
- [x] Model tiering (cheap model for compaction/summarization) — [S/additive]

### Wave 2 — Execution-security gating, transport hardening & context bounding
- [x] Flag-level read-only whitelist + injection denylist + path/device guards — [M/additive]
- [x] Transport-origin allowlist + remote kill-switch + lazy OS-permission acquisition — [M/additive]
- [x] Workspace-trust gate (git-root keyed) — [M/additive]
- [x] Double-buffered message snapshot for concurrent submits — [S/additive] (satisfied by existing infra — per-task goroutine + own transcript, not a rebuilt snapshot type; see `absorption-status.md`'s "Satisfied by existing infrastructure" note)
- [x] Tool-result disk offload + reference substitution + pagination signal — [M/additive] (landed as the content-addressed `go/artifact` store with bounded reads, `kilocode-absorption.md`; head+tail-aware preview truncation completed 2026-07-12, `cline-absorption.md`'s `mid_truncation`, commit `a8be846`)
- [x] Multi-tier compaction (token trigger + circuit breaker + verbatim-user + rebuild-with-key-files) — [M/additive] (partial: circuit-breaker was already done; token-threshold trigger landed 2026-07-12, `cline-absorption.md`'s `context_pruner_agent`, commit `5898e17`; verbatim-user preservation and the rebuild-with-key-files top tier benchmarked against codex/Claude Code 2026-07-12 — `codex-claudecode-benchmark.md`, verdict `defer` (downgraded from `adopt` by adversarial review: `EverModifiedFiles`/`addTurn` file-tracking mismatch and an unspecified hash-preimage rework for the verbatim partition, plus active same-seam concurrent work); deep-tradeoff closeout 2026-07-12: all three defer blockers verifiably gone (`Turn.Path` landed `38ba80e`, `CompactionReceipt.Version` exists with one preimage consumer, subagent-dsl merged), verdict flipped to land — verbatim-user preservation + v2 receipt + deterministic `keyFiles()` substrate implemented on branch `feat/absorb-multi-tier-compaction` @ `d1f7478657f0`, merged to main 2026-07-12; Part-B content reinjection deliberately scoped out (merge-hot files; self-healing failure mode))

### Wave 3 — Cost governance, model resilience, durable state registry
- [x] Cost & token metering with budget governance (pause-and-approve gate) — [M/additive]
- [x] Per-subagent task budget + resource caps (whale protection) — [S/additive]
- [x] Resilient model routing (cross-model fallback + typed-error retry + overflow auto-shrink + heartbeat) — [M/additive] (`go/daemon/reasoner.go`'s `retryGovernance`/`thinkWithRetry*`/`retryAfterFromError`/`retryDelay`)
- [x] Central side-effect registry + worker-reconnect state rehydration — [M/additive] (registry half satisfied by the hash-chained audit log + event bus, per existing infra note; worker-reconnect via the Wave 7 work-dispatch bridge's idempotent, ownership-checked reporting)
- [x] Versioned idempotent config/state migration — [S/additive] (no `STATE_VERSION`-style schema migration ladder found in `go/config`/`go/daemon` session or run stores as of this reconciliation — genuinely open, not just differently named; benchmarked against codex/Claude Code 2026-07-12 — `codex-claudecode-benchmark.md`, verdict `design_only`: reject the generic ladder both a naive reading of Codex and the seed implied, adopt Claude Code's idempotent value-inspecting upgrade pattern plus quarantine-not-delete on version mismatch instead; deep-tradeoff closeout 2026-07-12: the reserve slice landed — new leaf `go/statefmt` (Probe/ReadVersioned/Quarantine, only version>current quarantines via rename-not-delete) wired into the three object-envelope stores, fixing `usage.go`'s confirmed destroy-on-future-version defect; bare-array stores deliberately excluded (stamping them is a breaking shape change); implemented on branch `feat/absorb-state-migration` @ `32f0023e0300`, merged to main 2026-07-12; the upgrade-function ladder itself remains correctly deferred until a v2 schema exists)
- [x] Cross-process command/prompt history with chunked lazy load — [S/additive]

### Wave 4 — Agent surface: hooks / skills / plan / styles / memory + tool registry + structured output + prompt cache
- [x] buildTool() middleware seam (auto gate+audit+metrics) — [M/additive] (satisfied by existing infra — `dispatchAction` gates every tool via the kernel and records it, rather than a separate registry type)
- [x] Schema-validated structured output for headless runs — [S/additive]
- [x] Hooks lifecycle interception (Pre/Post/Stop + exit-2 blocking) — [M/additive]
- [x] Skills / slash-command system (governed prompt-workflows) — [M/additive] (landed as agent modes + slash commands, `absorption-status.md` Wave 11 OpenCode absorption)
- [x] Plan mode (propose -> approve -> execute gate) — [S/additive]
- [x] Output styles (layered policy-governed system-prompt composition) — [S/additive]
- [x] Persistent memory subsystem + hierarchical CARINA.md loading — [M/additive] (deepened well past the original scope by Wave 19's Hermes memory absorption)
- [x] Segmented prompt-cache architecture — [M/additive] (initial segmentation Wave 10; real per-provider stable/volatile boundary Wave 22)

### Wave 5 — Multi-agent coordination & advanced session lifecycle
- [x] Async steering of a running agent (durable mailbox + turn-boundary drain) — [M/additive] (two-tier urgent/normal priority added 2026-07-12, `cline-absorption.md`'s `steer_vs_queue_priority`, commit `1281f76`)
- [x] Leader permission bridge (bounded child->parent escalation) — [M/additive]
- [x] Coordinator restricted-orchestrator role + independent async verifier — [M/additive] (independent-verifier half: `go/daemon/verifier.go`, `absorption-status.md` Wave 9; restricted-orchestrator half closed as **already_covered** by the `feat/public-subagent-dsl` merge into main `da96a34` (deep-tradeoff closeout 2026-07-12): dedicated kernel-gated `Capability::SubagentSpawn` with typed `agent:NAME:profile:PROFILE` resource at the single spawn choke point, daemon-enforced `AgentSpec.ToolNames` allow-list denied pre-dispatch, and per-hop `AgentSpec.SpawnableAgents` enforcement keyed to the spawning session (structurally closing Claude Code's v2.1.186 nested-spawn bug), all with tests — profile=safe-edit + tool_names=spawn,done + spawnable_agents IS the coordinator shape; the prior Rust "orchestrator" Profile design was found mechanistically wrong (Profile has no spawn axis; `attenuate()` would make workers read-only too). Non-blocking follow-ups recorded: built-in coordinator `AgentSpec` preset, primary-session `ToolNames` binding gap, PolicyBundle differentiation on the spawn resource. Reopens if main's merge resolution drops the gates)
- [x] Task-notification loop-closing protocol (idempotent completion envelope) — [S/additive]
- [x] Intra-turn parallel tool execution with concurrency-safety partition — [M/additive]
- [x] Session fork-with-lineage + rewind-to-checkpoint — [M/additive] (fork-with-lineage Wave 5; rewind served by `session.checkpoint.restore`, not a function literally named "rewind")
- [x] Ephemeral non-polluting side query (/btw) — [S/additive]
- [x] Attach/tail with replay cursor + reconnect dedup — [M/additive]

### Wave 6 — Config cascade, auth chain, permission UX & operational hardening
- [x] Per-session setting-source allowlist + config/MCP-layer filtering — [M/additive] (a real precedence cascade landed — `go/config`'s defaults → global → project → env → flags, `absorption-status.md` Wave 8 — but the specific 4-layer Managed/User/Project/Runtime shape with managed-locked keys and untrusted-project-source filtering was not confirmed; benchmarked against codex/Claude Code 2026-07-12 — `codex-claudecode-benchmark.md`, verdict `defer` (downgraded from `adopt` by adversarial review: the threat model conflates the daemon's launch-time cwd config with per-task untrusted-repo config, which carina's existing `trustStore` already scopes correctly); deep-tradeoff closeout 2026-07-12: item split — the project-source-filtering half stays dead (objection conceded, `trustStore` owns it), the managed-locked-keys half landed (`go/config/managed.go`, `/etc/carina/managed.json` values + locked_keys, fail-closed on unknown/valueless locks and flag collisions, tighten-only re-apply after all layers, watch-path reload) on branch `feat/absorb-setting-source-allowlist` @ `0984bdb3e892`, merged to main 2026-07-12 — closes the gap where a local env var/flag could un-pin org posture keys incl. `policy_dir` itself)
- [x] Atomic-write-safe config/spec hot-reload — [M/additive]
- [x] Ordered multi-source auth chain + managed-context isolation + apiKeyHelper — [M/additive]
- [x] Scoped runtime capability grant (/add-dir) + config precedence cascade — [M/additive]
- [x] Interactive + remote permission request/resolve protocol — [M/additive]
- [x] Doctor/system-health surface — [S/additive]
- [x] Anti-tamper process hardening — [M/additive]

### Wave 7 — Large subsystems (pragmatic MVPs) & dependents
- [x] MCP interop: client + server mode — [L/large-subsystem]
- [x] Deferred lazy tool-schema + health-gated tool-pool + ToolSearch — [M/additive] (no evidence of a lazy/searchable tool-pool; carina's native tool surface is small and fixed rather than a large growing inventory the way Claude Code's is; benchmarked against codex/Claude Code 2026-07-12 — `codex-claudecode-benchmark.md`, verdict `design_only`: real, externally-corroborated gap scoped to the MCP surface (both sources went MCP-first before generalizing), design recorded (`go/mcp/search.go`, BM25-style local index), blocked on the not-yet-existing `buildTool()` abstraction and `agent.go` being hot; deep-tradeoff closeout 2026-07-12: both blockers falsified/dissolved (five tools incl. hours-old `best_of_n` landed as plain switch cases without `buildTool()`; the subagent-dsl merge completed mid-analysis leaving both wiring hunks byte-identical) — MCP-scoped stateless `mcp_find` (weighted token-overlap over name/description/schema, hidden servers excluded, full `InputSchema` on demand) implemented on branch `feat/absorb-tool-pool-toolsearch` @ `e0a82b57bd91`, merged to main 2026-07-12; health-gated pool assembly excised for its own future review)
- [x] Direct-connect HTTP+WebSocket session API — [M/additive] (landed as the OpenClaw Gateway WS + HTTP transports, `absorption-status.md` Waves 13–17, rather than a bespoke NDJSON/WS front)
- [x] Distributed work-dispatch bridge (poll + lease + attenuated creds) — [L/large-subsystem]
- [x] Egress proxy (network as gated capability + credential injection) — [L/large-subsystem]
- [x] OS-level syscall sandbox (seccomp/namespaces, sandbox-exec/SBPL) — [L/large-subsystem] (macOS Wave 7, Linux bubblewrap Wave 9)
- [x] Post-edit diagnostics-delta feedback loop + LSP intelligence — [L/large-subsystem] (Stage 1 Wave 7, Stage 2 LSP Wave 9; line-shift-tolerant pre/post delta added 2026-07-12, `cline-absorption.md`'s `pre_post_diagnostics_diff`, commit `1c778bf`)
- [x] Composable plugin bundles + git marketplace + tri-level enable merge — [M/additive] (a real extension/marketplace system exists — `go/extensions`, trusted-roots-scoped, referenced in `daemon.go` as "extension marketplace" — but git-clone install specifically and tri-level enterprise-tighten-only merge were not confirmed against code; benchmarked against codex/Claude Code 2026-07-12 — `codex-claudecode-benchmark.md`, verdict `defer` (downgraded from `adopt` by adversarial review: the load-bearing claim that ed25519 signature verification could reuse existing-but-unused kernel machinery, `carina-plugin-runtime::SignatureVerifier`, is factually wrong about what that machinery does; the tri-level enable-merge sub-piece is sound and could be split off and adopted independently in a future pass); deep-tradeoff closeout 2026-07-12: exactly that split executed — the git-marketplace + signing half stays rejected (`SignatureVerifier` objection conceded; git-clone is a new trust surface needing its own pass), while the tri-level enable-merge (safe_mode > org > project > user; org tier from `<PolicyDir>/extensions.json` with startup force-disable reconcile and typed `ErrOrgDisabled`, project tier disable-only and never persisted, `effective_enabled` + `enable_provenance` in inventory) landed on branch `feat/absorb-plugin-bundles-marketplace` @ `5e4a7ac16a92`, merged to main 2026-07-12 — the prerequisite org-config channel (`PolicyDir → loadOrgPolicy`) was verified already live)
- [x] Content-block (image) support — [M/additive] (split 2026-07-12 from the original bundled "image + dynamic skill prompts" line so each half tracks honestly. Landed in three slices, all same-day: (1) MediaRef plumbing — `go/daemon/media.go`: sha256 content-addressed refs into `artifact.Store`, strict magic-byte allowlist fail-closed, placeholder-only rendering so bytes never enter prompt/transcript/checkpoint by construction — the invariant Claude Code regressed twice, v2.1.157/v2.1.187 — branch `feat/absorb-content-block-images` @ `b8c31c9a8e07`, merged to main; (2) producer — reading an image file ingests into the artifact store and yields placeholder+MediaRef on both dispatch paths, never binary in the transcript; (3) delivery — catalog-gated (`Modalities.Input` must affirmatively declare image; fail-closed on empty/unknown/default model strings) `collectRequestMedia` resolves live non-elided refs (caps 4 parts/4 MiB) into the model call via a `mediaSegmentedReasoner` capability-upgrade interface, with Anthropic base64-source, OpenAI chat `image_url`, OpenAI responses `input_image`, and Gemini `inline_data` encodings; text-only reasoners degrade to placeholders. Producer+delivery commit `0be9b2a`. The earlier kill criterion — "delete the unproduced field if no producer arrives within two passes" — is discharged: the producer exists)
- [x] Context-aware dynamic skill prompts — [M/additive] (closed 2026-07-12 in `go/daemon/skill_prompts.go`: user/project `SKILL.md` discovery with project precedence, bounded deterministic metadata catalog plus truncate-and-warn, `$name` and collision-safe `/name` explicit invocation, opt-in exact-trigger implicit invocation, safe-mode/disabled/malformed/not-user-invocable fail-closed warnings, stable-prefix body injection with de-duplication and no classifier/model call. `disable-model-invocation` and `user-invocable` match the Claude Code routing matrix; declared `allowed-tools` remains non-granting guidance under the kernel. Covered by `skill_prompts_test.go`, including prompt-cache stability and budget behavior; see `codex-claudecode-benchmark.md`)
