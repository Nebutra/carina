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

**Wave 7 — large subsystems (production-grade)**
- [x] **MCP interop — client** (`go/mcp`): stdio JSON-RPC 2.0, async reader + per-call timeouts, initialize/tools-list/tools-call lifecycle, reconnect, `mcpServers` config; the `mcp` tool proxies every call through the capability kernel (PluginLoad) + audit, tools surfaced as `mcp__server__tool`. (Also fixed the stale `PluginLoad=Denied` policy → `RequiresApproval`, unblocking spawn in production.)

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

**Large subsystems (remaining — each its own focused effort):**
- MCP **server mode** (expose Carina's gated tools to other MCP clients) — client done above
- Distributed work-dispatch bridge (scheduler leases + worker poll-execute-report)
- Egress proxy (network as a gated capability + boundary credential injection)
- OS-level syscall sandbox (macOS sandbox-exec/SBPL, Linux namespaces+seccomp)
- Post-edit diagnostics-delta + LSP semantic intelligence

## Test status
`carina-policy` unit tests green; `go/rpc`, `go/scheduler`, `go/kernel` green;
all newly-added daemon feature tests green. The only red/skipped Go tests
(`TestGoalSuccessCriteriaVerified`, `TestRBACApprovalRequiresRole`,
`TestDaemonHandlerSurface`, `TestEndToEndLoop`) require the Zig toolchain, which
is not installed on the dev box; they pass in CI and are unrelated to this work.
