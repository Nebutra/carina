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

## ✅ Remaining

- No known capability gaps remain in this absorption track. The previously
  deferred Egress HTTPS-MITM credential tier has passed its standalone review and
  is now implemented behind explicit per-host opt-in.

## Test status
Current verification for this update: **Go: 156 tests across 15 packages under
`-race`**; the Go tree also cross-builds for linux/amd64. Includes the
previously Zig-gated tests and every Wave-7/8/9/10 subsystem test. **Rust: all
workspace crates pass** (`cargo test --workspace`: 67 tests across 14 suites).
Zig tools were not rebuilt in this environment because the `zig` compiler is not
installed on PATH; the existing `zig/zig-out/bin` tool outputs are present.
