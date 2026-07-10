# Carina — Absorption Status

Tracking which Claude Code gaps (from `claude-code-gap-analysis.md`, sequenced in
`absorption-plan.md`) are absorbed. Every item below shipped as a tested commit.

## ✅ Done — this campaign (waves 1–6 core)

**Wave 1 — loop / IO correctness**
- [x] Shell compound-command decomposition + hardening (`&&·||·|·;·\n` + `$()`/backtick + background `&` + `<()`/`>()` process-sub + `eval`/`xargs` descent; max-risk over segments)
- [x] Read-before-write dirty-write guard (blind/stale overwrite refused)
- [x] Duplicate-key JSON rejection (fail-closed; wired into workflow-spec parsing)
- [x] Model tiering (cheaper summarizer model for compaction)
- [x] Tool-error-as-result + WAL ordering (confirmed already satisfied)

**Wave 2 — kernel / transport hardening**
- [x] Flag-level whitelist + injection/redirect denylist + git-write classification
- [x] Device/special-path guard (`/dev`,`/proc`,`/sys`,FIFO,socket,UNC,NUL)
- [x] Transport-origin restriction + remote kill-switch (TCP → read/observe + worker protocol only)
- [x] Workspace-trust gate (opt-in strict mode)

**Wave 3 — cost / budget**
- [x] Per-task token budget governor (over-budget → graceful degrade)
- [x] Per-subagent token budget (whale-session protection)

**Wave 4 — agent surface**
- [x] Hooks lifecycle interception (PreToolUse/PostToolUse, exit-2 block + stderr feedback)
- [x] Persistent memory / hierarchical CARINA.md loading
- [x] Schema-validated structured output for headless runs
- [x] Output styles (layered system-prompt composition)
- [x] Plan mode (read-only until the plan is approved)

**Wave 5 — coordination / session lifecycle**
- [x] Async steering mailbox (redirect a running/background agent via `task.steer`)
- [x] Session fork-with-lineage (`session.fork`)

**Wave 6 — ops**
- [x] Doctor / system-health surface (`daemon.doctor` independent probes)

**Wave 7 — large subsystems (production-grade, all landed)**
- [x] **MCP interop — client** (`go/mcp`): stdio JSON-RPC 2.0, async reader + per-call timeouts, initialize/tools-list/tools-call lifecycle, reconnect, `mcpServers` config; the `mcp` tool proxies every call through the capability kernel (PluginLoad) + audit, tools surfaced as `mcp__server__tool`. (Also fixed the stale `PluginLoad=Denied` policy → `RequiresApproval`, unblocking spawn in production.)
- [x] **MCP interop — server mode** (`go/mcpserver` + daemon adapter): Carina speaks MCP as a *server*, exposing `list/read/search/run/patch` (with JSON schemas) to any MCP client. Every `tools/call` maps onto `executeAction`, inheriting the SAME kernel + hooks + plan-mode gate as the agent loop. Tool failures → isError content; unknown methods → JSON-RPC errors (spec-correct). `Daemon.ServeMCP` serves one session over a stream.
- [x] **Egress proxy** (`go/egress`): deny-by-default loopback HTTP/CONNECT forward proxy; network is a gated capability via `Gate`. `carina-run` forwards `HTTP(S)_PROXY/NO_PROXY` into command children so the boundary applies end-to-end.
- [x] **OS syscall sandbox** (`carina-run --sandbox`): macOS `sandbox-exec`/SBPL profile confining file writes to the workspace (realpath'd cwd) + `/tmp` — a syscall-level safety net orthogonal to the kernel policy; wired through `toolchain.Run` + `Options.SandboxCommands`. (Linux namespaces+seccomp is the next platform.)
- [x] **Work-dispatch bridge** (`go/scheduler` lease layer + daemon `work.*` RPC): reliable lease queue for remote workers — register → poll → execute → report → heartbeat. Visibility-timeout leases + background reaper (crashed-worker recovery), idempotent + ownership-checked reporting (at-least-once). Enqueue is control-plane/local-only; poll/renew/report are remote-safe.
- [x] **Post-edit diagnostics** (`go/daemon/diagnostics.go`, LSP Stage 1): after a patch, a fast language probe (`gofmt -e`/`py_compile`/`node --check`/`rustc`) feeds introduced compile/parse errors back into the observation for same-turn self-correction. (Full LSP semantic deltas — gopls/tsserver — is a later stage.)

> Plus, earlier in the same effort: the **Workflow orchestration engine** (DAG +
> parallel + resume) and **background runs** (durable run registry, per-turn
> transcript checkpoint + restart-resume, concurrency cap, panic isolation).

## ➖ Satisfied by existing infrastructure (no rebuild)
- Central side-effect registry ≈ the hash-chained audit log + event bus (every effect recorded).
- Double-buffered submit snapshot ≈ per-task goroutine + own transcript (no shared context).
- buildTool() middleware seam ≈ `dispatchAction` gates every tool via the kernel and records it.

**Wave 8 — coordination / config / security close-out (all landed)**
- [x] **Task-completion notification envelope** (`go/daemon/notify.go`): terminal
  paths (finish/degrade/cancel/remote-report) publish one `task.completed` signal
  (status, summary, patches, tokens, attempts, duration); plus `Bus.Tap` for
  in-process observation (parent/child coordination, metrics).
- [x] **`/add-dir` scoped grant** (`kernel.session.add_dir`): widen a session to
  additional roots without loosening the profile — the kernel evaluates each path
  capability against its containing root (effective_root); local-only grant.
- [x] **Config precedence cascade** (`go/config`): defaults → global → project →
  env → flags, unknown-key/typo rejection, fail-fast validation; wired into the
  daemon entrypoint so all newer knobs are file/env configurable.
- [x] **Interactive permission request/resolve** (`go/daemon/approval.go`): opt-in
  human-in-the-loop — `requires_approval` pauses (waiting_approval), emits a
  `permission.request`, and blocks on `task.approval.resolve` (allow/deny) or a
  timeout (=> denied); autonomous auto-approve stays the default.
- [x] **Attach/tail replay cursor** (`session.attach`): cursor-based replay for a
  reconnecting client (catch up from a monotonic cursor, then tail live).

**Wave 9 — deepening close-out (all landed, workflow-designed)**
- [x] **Egress boundary credential injection** (`go/egress/inject.go`): the proxy
  injects a per-host header from a daemon-side secret at the boundary; the agent's
  children never see it (carina-run's env allowlist). Plain HTTP injects directly;
  HTTPS injection is now an explicit per-host MITM opt-in (`MITM: true`) with an
  ephemeral in-memory CA, per-host leaf certificates, verified proxy-to-upstream
  TLS, and a process-local child trust bundle.
- [x] **LSP semantic diagnostics** (`go/lsp`): a real LSP client (Content-Length
  framing, initialize/didOpen handshake) surfaces type errors beyond the Stage-1
  syntax probe; no-op when no server is installed. Tested via a mock LSP server.
- [x] **Linux sandbox backend** (`carina-run`): bubblewrap namespace sandbox
  parallel to macOS sandbox-exec; compiler-verified for x86_64-linux (cross-build).
- [x] **Intra-turn parallel tools** (`go/daemon/agent.go`): `{"actions":[...]}`
  runs read-only tools (list/read/search) concurrently in one turn; writes stay
  one-per-turn (rejected in a batch), so no write races.
- [x] **Coordinator/verifier separation** (`go/daemon/verifier.go`): an
  independent judge (fresh context) rules on the done-claim before finish;
  default-lenient + fail-open.
- [x] **Leader permission bridge** (`go/daemon/bridge.go`): a subagent escalates a
  refused whitelisted capability to its parent (child ⊆ parent preserved),
  bounded by whitelist + one-hop + per-task cap.
- [x] **Config hot-reload** (`go/daemon/reload.go`): SIGHUP live-applies the
  reloadable subset (budget, approval mode, trust, sandbox, egress allowlist) via
  atomics + egress SetGate; validate-before-apply keeps last-good.

**Wave 10 — niche close-out (all landed)**
- [x] **Ordered multi-source auth chain** (`go/auth`): BYOK API keys first
  (env/static/file), then a Nebutra-ecosystem OAuth fallback; Kind drives the
  header (x-api-key vs Authorization: Bearer). Values never logged; daemon.doctor
  reports the resolved source name only.
- [x] **Config fs-watch** (`go/config/watch.go`): dependency-free mtime-poll
  auto-reload on top of SIGHUP.
- [x] **/btw ephemeral side-query** (`task.btw`): answers an aside in task context
  without polluting the transcript (side_query audit event only).
- [x] **Prompt-cache segmentation** (`go/daemon/promptcache.go`): stable prefix
  (system+task) vs volatile suffix (transcript) with a CacheBreakpoint for
  provider prefix-caching; byte-identical prompt (pure refactor).
- [x] **Cross-process history** (`go/history`): O_APPEND shared prompt history
  safe across processes; `history.recent` RPC.
- [x] **Anti-tamper hardening** (`go/daemon/harden_linux.go`): Linux prctl
  PR_SET_DUMPABLE 0 (non-dumpable, anti-ptrace) protecting in-memory secrets;
  build-tagged no-op off Linux.
- [x] **Expanded LSP matrix**: rust-analyzer/clangd/zls/solargraph alongside
  gopls/tsserver/pyright.

**Wave 11 — OpenCode absorption (all landed)**
- [x] **BYOK provider catalog + runtime adapters** (`go/provider`,
  `go/model-router`, `go/daemon/provider_adapters.go`): provider metadata is no
  longer a thin hard-coded enum. Carina now has a discoverable catalog with
  env-key lookup, model IDs, API-family routing, credential resolution, and
  per-request model dispatch for OpenAI-compatible, Anthropic, Gemini, and
  OpenRouter-style providers.
- [x] **Agent modes + slash commands** (`go/daemon/agents.go`,
  `go/daemon/commands.go`): built-in/user/project agents are discoverable via
  `agent.list`, task submission accepts `--agent`, and slash commands expand
  reusable prompt templates from built-in, user, and project registries.
- [x] **MCP prompts as command registry entries** (`go/mcp`,
  `go/daemon/mcp_commands.go`): external MCP `prompts/list` metadata is exposed
  as `/mcp.<server>.<prompt>` commands, and `task.submit` renders them through
  `prompts/get` before scheduling. Prompt-only MCP servers now connect cleanly;
  prompt expansion is read-only and does not grant MCP tool capabilities.

**Wave 12 — OpenAI Codex source absorption (landed)**
- [x] **Canonical session item stream** (`go/daemon/items.go`): Codex's strongest
  reusable mechanism is not its Rust workspace shape or cloud coupling, but the
  projection layer that turns raw runtime notifications into stable
  `thread.started` / `turn.started` / `item.*` / `turn.completed` events.
  Carina now exposes `session.items` and `carina items <session_id>` as a
  derived, non-authoritative view over the existing hash-chained audit log.
  Command lifecycle events are grouped into `command_execution`, model replies
  into `agent_message`, patch lifecycle into `file_change`, and terminal task
  status into turn completion/failure. New command events include `command_id`
  for precise future correlation; old logs remain order-compatible.
- [x] **Phase A project instructions + provider cache strategy**
  (`go/daemon/memory.go`, `go/provider/catalog.go`): Codex's AGENTS.md and model
  manager mechanisms were absorbed by philosophy, not copied by brand. Carina
  now loads Nebutra/Carina project instructions from repo root to workspace
  (`CARINA.override.md` / `CARINA.md` first, `AGENTS.override.md` / `AGENTS.md`
  as compatibility fallback) with source labels and budget truncation. Provider
  discovery now has explicit `online` / `offline` / `online_if_uncached`
  strategies, a versioned cache envelope (`fetched_at`, `etag`, `catalog`),
  ETag/304 TTL renewal, and legacy plain-cache compatibility.
- [x] **Phase B Nebutra Risk Review for autonomous approvals**
  (`go/daemon/risk_review.go`, `go/daemon/approval.go`): Codex Guardian's useful
  philosophy was absorbed as an approval reviewer that can only tighten kernel
  decisions. The kernel still decides `denied` / `allowed` /
  `requires_approval`; Risk Review runs only on autonomous `requires_approval`
  upgrades, after the kernel and before `ApproveWithRole("agent")`. Modes are
  `off`, default `advisory` (record only), and `enforce` (deny blocks auto
  approval). The default reviewer is deterministic/local; an optional
  `risk_review_model` / `CARINA_RISK_REVIEW_MODEL` reviewer can produce the
  same JSON assessment. Each review is audited as `TaskCreated` with
  `status=risk_review` and is projected into `session.items` as a
  `risk_review` item.
- [x] **Phase C approval overlays with justifications**
  (`crates/carina-kernel`, `go/kernel`): Codex's execpolicy overlay philosophy
  was absorbed into Carina's existing session approval memory. A session
  approval is now an explicit overlay with capability, resource prefix, source
  decision id, approver, justification, and creation time. Overlays only satisfy
  future `requires_approval` decisions; they never rescue `denied` policy
  results. Overlay creation and overlay hits are audit-visible, so repeated
  approvals are explainable instead of silent cache behavior.
- [x] **Phase C turn-level net diff projection** (`go/daemon/items.go`):
  `session.items` now derives `turn_net_diff` items by correlating
  `PatchProposed`, `PatchApplied`, `PatchFailed`, and `RollbackCompleted`
  events by `patch_id`. Applied patches contribute active files; rolled-back
  patches are shown as reverted rather than active net changes. This remains a
  non-authoritative projection over the hash-chained audit log.

**Wave 13 — OpenClaw Gateway absorption (Phase A landed)**
- [x] **Descriptor-first RPC control plane** (`go/rpc`, `go/daemon`): absorbed
  OpenClaw Gateway's strongest control-plane philosophy without porting its
  TypeScript surface. Carina daemon methods now register through
  machine-readable descriptors (`method`, `scope`, `remote`, `stream`,
  `advertise`, `control_plane_write`), remote exposure is derived from that
  same catalog, and daemon strict mode refuses unclassified handlers. The new
  `gateway.methods` RPC plus `carina gateway methods` expose the live catalog
  for CLI/UI/future WS `hello-ok` feature discovery. Follow-on phases remain
  separate: role/scoped WebSocket handshake, agent-first OpenAI-compatible HTTP,
  scoped `/tools/invoke`, plugin HTTP request scopes, and Nebutra-boundary
  device/node pairing.

**Wave 14 — OpenClaw Gateway absorption (Phase B landed)**
- [x] **Handshake skeleton + dynamic scopes** (`go/rpc`, `go/daemon`,
  `apps/carina-cli`): added a transport-neutral `gateway.hello` contract for
  role/scope/feature discovery, exposed `carina gateway hello [role]`, and
  extended descriptors with `dynamic_scope`. `gateway.resolve_scope` resolves
  effective scope from params for diagnostics and future transports. The first
  param-sensitive rule is `workspace.patch.propose`: normal relative patch
  paths resolve to `write`; empty, absolute, `.`, or `..` paths resolve to
  `admin`. This keeps the OpenClaw philosophy while leaving actual network
  Gateway, `/v1`, and `/tools/invoke` for later phases.

**Wave 15 — OpenClaw Gateway absorption (Phase C landed)**
- [x] **Default-off WebSocket Gateway skeleton** (`go/rpc`, `go/daemon`,
  `apps/carina-daemon`, `go/config`): added an explicit `-gateway-ws` /
  `gateway_ws` / `CARINA_GATEWAY_WS` listener at `/gateway`, disabled unless
  configured. The first text frame must be `gateway.hello`; later JSON-RPC
  frames reuse descriptor `remote`, the remote kill-switch, negotiated
  role/scopes, and dynamic scope resolution. Browser `Origin` headers are
  rejected unless exactly allowlisted through `-gateway-ws-origins`,
  `gateway_ws_origins`, or `CARINA_GATEWAY_WS_ORIGINS`. This gives Carina a
  real OpenClaw-style Gateway transport shell without adding `/v1`,
  `/tools/invoke`, new auth grants, or Nebutra device pairing.

**Wave 16 — OpenClaw Gateway absorption (Phase D landed)**
- [x] **Scoped Gateway capability tokens** (`go/rpc`, `go/daemon`,
  `go/config`, `apps/carina-daemon`): added signed `gw1` role/scope/transport/
  expiry claims, local-only `gateway.token.issue`, explicit signing-key config,
  private key-file validation, max TTL, and WS hello verification when the
  signer/verifier is configured. Empty token scopes fail closed; verifier
  rejects non-canonical signed claims, tampering, expiry, and transport
  mismatch. The signing key is never accepted as a bearer credential.
- [x] **WebSocket stream coverage + CLI probe** (`go/rpc`,
  `apps/carina-cli`): WebSocket tests now cover stream subscription
  notifications after `gateway.hello`, and `carina gateway ws-probe <ws-url>
  [role]` performs a direct stdlib WS handshake and prints the hello response.
- [x] **Dynamic scope expansion** (`go/daemon`): `session.add_dir`,
  `workspace.trust`, and `task.action.deny` now resolve param-sensitive scopes;
  low-risk contained/revocation/ordinary-deny cases are `write`, while
  ambiguous, outside, granting, spoofed-approver, or approval paths stay
  `admin`.
- [x] **Agent-first HTTP/tool/plugin skeletons and Nebutra pairing boundary**
  (`docs/rpc-api.md`, `docs/plans`,
  `docs/nebutra-cloud-boundary.md`): reserved `/v1`, `/tools/invoke`, and
  plugin HTTP request-local Gateway scope as disabled future surfaces gated by
  scoped Gateway tokens. Device/node pairing remains a Nebutra identity/sync
  boundary, not local action authority; node commands must be declared,
  filtered, bounded, audited, and scoped.

**Wave 17 — Product/runtime closure (landed)**
- [x] **Default-off HTTP Gateway runtime** (`go/daemon`, `go/config`,
  `apps/carina-daemon`): added `gateway_http` / `CARINA_GATEWAY_HTTP` /
  `-gateway-http`, with exact browser-origin allowlisting and fail-closed
  startup unless scoped token signing is configured. HTTP tokens must be bound
  to `transport: "http"` and carry explicit route grants in addition to scopes.
- [x] **Agent-first `/v1` runtime** (`go/daemon`): `/v1/models` lists Carina
  agent targets instead of provider catalogs; `/v1/chat/completions` and
  `/v1/responses` submit normal Carina tasks through the existing daemon path
  and return OpenAI-shaped envelopes with task/session metadata.
- [x] **Scoped `/tools/invoke` runtime + plugin HTTP fail-closed contract**
  (`go/daemon`): `/tools/invoke` is limited to a read-only allowlist and still
  uses existing daemon/kernel read paths. `/plugins/*` is authenticated but
  returns fail-closed until a plugin route contract exists.
- [x] **Minimal usable TUI** (`apps/carina-tui`): replaced the placeholder with
  a read-only status/session viewer over the daemon socket.

**Wave 18 — Release/install packaging closure (landed)**
- [x] **Local release candidate packaging** (`Makefile`,
  `scripts/package-release.sh`): added `make release-package` for
  current-platform archives under `dist/`, including Go CLIs, the Rust kernel
  service, Zig `carina-*` native tools, release docs, per-file checksums,
  archive checksum, `MANIFEST.json`, and `VERSION_CHECK.txt`.
- [x] **Version and build transparency** (`scripts/package-release.sh`): package
  version defaults to the CLI version or explicit `VERSION=...`; daemon, Rust
  workspace, TypeScript SDK, and Python SDK version mismatches are recorded as
  warnings instead of hidden. `SKIP_BUILD=1` and `SKIP_ZIG=1` are explicit and
  also recorded in the package manifest.
- [x] **Install-channel templates without false publication claims**
  (`packaging/homebrew`, `packaging/npm`): added Homebrew and npm templates as
  publish-time scaffolding. The Homebrew template is now rendered by the
  tag-driven macOS release workflow into `Nebutra/homebrew-tap`; npm remains
  unpublished.
- [x] **README/release/roadmap sync** (`README*.md`, `docs/release.md`,
  `docs/roadmap.md`): documented the alpha state, local package command,
  package verification, live macOS Homebrew channel, and remaining release
  gaps.

**Wave 19 — Hermes Agent memory absorption (landed)**
- [x] **Governed local long-term memory** (`go/daemon/memory_store.go`):
  absorbed Hermes' useful memory philosophy without importing its Python
  monolith. Carina now has a local bounded memory store under the daemon state
  directory, with separate `memory` (agent/project notes) and `user` (profile
  facts) targets, add/replace/remove/batch operations, atomic writes, duplicate
  handling, size limits, and deterministic threat-pattern rejection for
  persistent prompt injection, exfiltration, backdoors, agent config mutation,
  and hardcoded-secret capture.
- [x] **Frozen per-run memory snapshot** (`go/daemon/agent.go`,
  `go/daemon/runstore.go`): memory is loaded once per run as background
  context in the stable prompt prefix. Writes during the run persist for future
  work but do not mutate the current run's prompt snapshot or transcript.
- [x] **Native memory action + local RPC surface** (`go/daemon`):
  agents can call the native `memory` tool; operators can use local-only
  `memory.list`, `memory.context`, and `memory.write` RPC. `memory.context`
  renders fenced recalled context that is explicitly not new user input.
- [x] **Kernel-gated `MemoryWrite` capability** (`crates/carina-policy`,
  `crates/carina-kernel`, `protocol/capabilities`,
  `protocol/schemas/permission-decision.schema.json`): memory mutation is now
  a first-class capability. Built-in policy requires approval by default, and
  policy bundles can still deny it explicitly via `deny_capabilities`.
  `memory.write` queues pending writes when approval is required and applies
  them only after
  `task.action.approve`.
- [x] **Memory audit hygiene** (`go/daemon`): extra audit payloads record
  target, scope, action, operation count, content hash, and decision id rather
  than raw memory text. Nebutra Cloud memory sync remains explicitly out of
  scope until a Nebutra identity/sync connector exists.
- [x] **Memory product closure** (`go/daemon`, `apps/carina-cli`,
  `docs/nebutra-cloud-boundary.md`): `target=user` now scopes by Nebutra
  canonical identity JSON, Nebutra OIDC/JWT claims, or local fallback, with
  hash profile keys for paths and audit resources. Operators get
  `carina memory status/list/context/write`; `memory.status` exposes local
  storage, external semantic-provider status, and Nebutra sync status. Approval
  deny-path tests confirm rejected memory writes do not persist.

**Wave 20 — OpenSquilla mechanism absorption (landed)**
- [x] **Routing decision/outcome evidence** (`go/daemon/agent.go`,
  `protocol/events`): every model attempt records requested model, reasoner,
  routing policy, prompt hash, response hash, evidence id, estimated
  input/output tokens, latency, and success/failure as separate events. This
  supplies the contract for future shadow evaluation without importing
  OpenSquilla's ML router or automatic promotion stack.
- [x] **Curated local memory retrieval** (`go/daemon/memory_store.go`): added
  deterministic lexical ranking and optional BYOK-embeddings semantic ranking
  over capability-approved `memory` and `user` entries through local-only
  `memory.search` and the CLI. Raw turns and audit transcripts remain excluded
  from recall.
- [x] **Persistent schedules** (`go/scheduler/schedules.go`,
  `go/daemon/schedules.go`): durable local persistence now supports `at`,
  `every`, and five-field `cron`, with create/list/pause/resume/delete RPC and
  CLI surfaces. Writes use temp-file fsync, atomic rename, directory sync,
  deterministic ordering, and corrupt-file quarantine. Due runs re-enter normal
  task submission, preserving write-ahead audit, kernel policy, budgets,
  checkpoints, and completion.
- [x] **Compaction receipts** (`go/daemon/transcript.go`): successful summary
  compaction persists a versioned receipt with covered turn range, removed
  count, preimage SHA-256, summary SHA-256, and timestamp. Receipts survive
  checkpoints and emit `ContextCompacted`; failed summaries keep old history.
- [x] **Selective absorption boundary**: Carina did not import OpenSquilla's
  Python monolith, model artifacts, automatic self-training/promotion,
  all-pipeline fail-open behavior, raw-turn memory capture, or process-local
  security state. The Rust capability kernel remains authoritative.
- [x] **Shutdown and compatibility closure** (`go/daemon/daemon.go`,
  `go/daemon/protocol_consistency_test.go`): daemon-owned background loops and
  submitted task goroutines are joined briefly on close, while protocol tests
  lock event enum/schema consistency and the new memory/schedule RPC surface.

**Wave 21 — Backpressure and diagnostic side-channel closure (landed)**
- [x] **Adaptive backpressure signal** (`go/daemon/backpressure.go`,
  `go/daemon/dispatch.go`, `protocol/jsonrpc/methods.json`): workers can
  report queue depth, inflight work, memory pressure, process load, estimated
  drain time, and monotonic sequence ids through `backpressure.report`.
  Daemon returns a TTL-bound advisory `ThrottleDirective`; `work.poll` only
  pauses leasing when the directive explicitly sets `max_inflight=0`. The
  signal is not a scheduler scoring input, authorization input, memory input,
  or route-promotion signal.
- [x] **Backpressure observability** (`daemon.status`, `daemon.metrics`,
  `backpressure.status`, `apps/carina-cli`): status exposes only compact
  counts, while `backpressure.status` shows current reports, directives, TTL,
  dispatch depth, task counts, and worker count. `carina backpressure status`
  gives operators a direct diagnostic surface without scraping audit logs.
- [x] **Non-authoritative debug attribution side-channel**
  (`go/daemon/debug_trace.go`, `apps/carina-daemon`, `apps/carina-cli`):
  `debug.snapshot` and `debug.correlation.search` expose a fixed-capacity
  in-memory ring buffer for local admin diagnostics only. It is disabled by
  default (`enable_debug_rpc=false` / `CARINA_ENABLE_DEBUG_RPC=false` /
  no `-debug-rpc`) and, while disabled, collection is also off.
- [x] **Debug authority boundary**: debug trace events are not persisted, not
  exported into the hash-chain audit stream, not used by permission, memory,
  route, or scheduling decisions, and are unavailable on the remote RPC
  allowlist. They can explain behavior during incident triage, but cannot
  become evidence or policy input.

## ✅ Remaining

- No known capability gaps remain in the Claude Code absorption track. The
  previously deferred Egress HTTPS-MITM credential tier has passed its
  standalone review and is now implemented behind explicit per-host opt-in.
- OpenCode items reviewed and intentionally not absorbed now: ACP session
  protocol support (overlaps Carina's JSON-RPC/CLI control plane) and broad
  workspace revert checkpoints (requires a separate snapshot policy).
- OpenAI Codex items reviewed and intentionally not absorbed now: ChatGPT/cloud
  app-server coupling remains outside Carina. Multi-endpoint identity/sync is
  now documented and guarded as a Nebutra Cloud (云毓智能, `nebutra.com`) product
  boundary with local sync off by default.
- OpenClaw Gateway items intentionally staged after Wave 18: Nebutra
  device/node pairing remains a Nebutra identity/sync product surface rather
  than local action authority. Full plugin HTTP route installation and
  write-capable direct tool invoke remain future work behind manifest policy
  and local owner review.
- BYOK semantic memory search is available only for curated local entries when
  an embeddings provider is configured. Nebutra Cloud memory sync remains an
  explicit disabled-by-default product boundary via `memory.status`; shipping
  that backend still requires a Nebutra connector with identity, deletion,
  retention, and conflict policy.
- OpenSquilla ML routing remains intentionally deferred until routing evidence
  has enough independently verified samples for shadow evaluation, canarying,
  and automatic rollback. The shipped contract is evidence-first and does not
  self-train on model-judged success.
- OpenSquilla-style implicit single-process backpressure and debug logs were
  intentionally not absorbed. Carina now has explicit TTL/seq backpressure and
  a local-only non-authoritative debug side-channel instead.

## Test status
Current verification for the OpenSquilla/backpressure/debug absorption update:

- `git diff --check`
- `jq empty protocol/jsonrpc/methods.json protocol/events/events.json protocol/schemas/event.schema.json`
- `CARINA_KERNEL_BIN=$PWD/target/debug/carina-kernel-service go test ./go/... ./apps/...`
- `cargo test -p carina-audit -p carina-kernel`
