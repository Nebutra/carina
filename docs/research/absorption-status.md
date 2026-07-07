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

## ✅ Remaining

- No known capability gaps remain in the Claude Code absorption track. The
  previously deferred Egress HTTPS-MITM credential tier has passed its
  standalone review and is now implemented behind explicit per-host opt-in.
- OpenCode items reviewed and intentionally not absorbed now: ACP session
  protocol support (overlaps Carina's JSON-RPC/CLI control plane) and broad
  workspace revert checkpoints (requires a separate snapshot policy).
- OpenAI Codex items still queued for separate absorption: execpolicy-style
  prefix overlays with justifications and turn-level net diff. ChatGPT/cloud
  app-server coupling remains intentionally outside Carina; multi-endpoint
  identity/sync work should use Nebutra (云毓智能) boundaries.

## Test status
Current verification for this update: full Go coverage
(`go test ./go/... ./apps/...`, 201 tests across 20 packages) and targeted race
coverage (`go test -race ./go/daemon ./go/config ./apps/carina-daemon`, 117
tests across 3 packages). This update touched Go control-plane approval/config
paths, daemon CLI flags, item projection, and docs only; Rust and Zig were not
rebuilt.
