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

## ⏳ Remaining — next dedicated phase

**Medium (additive, tractable):** leader permission bridge (bounded child→parent
escalation), coordinator/verifier separation, task-notification completion
envelope, intra-turn parallel tool execution, `/add-dir` scoped grant, config
precedence cascade + hot-reload, ordered multi-source auth chain, interactive
permission request/resolve protocol, prompt-cache segmentation, `/btw` ephemeral
side-query, cross-process history, attach/tail replay cursor, anti-tamper
process hardening (Linux prctl).

**Large subsystems:** all six landed (Wave 7 above). Remaining深化 (deepening,
each optional): Linux sandbox backend (namespaces+seccomp) alongside the macOS
one; full LSP semantic intelligence (gopls/tsserver live deltas) beyond the
Stage-1 syntax probe; boundary credential injection at the egress proxy.

## Test status
Full Go suite green **with the Zig toolchain built** (`zig build` →
`zig/zig-out/bin`): `carina-policy`, `go/rpc`, `go/scheduler`, `go/worker`,
`go/kernel`, `go/mcp`, `go/mcpserver`, `go/egress`, `go/toolchain`, and the full
`go/daemon` suite all pass under `-race`, including the previously Zig-gated
tests (`TestGoalSuccessCriteriaVerified`, `TestRBACApprovalRequiresRole`,
`TestDaemonHandlerSurface`, `TestEndToEndLoop`) and every Wave-7 subsystem test.
