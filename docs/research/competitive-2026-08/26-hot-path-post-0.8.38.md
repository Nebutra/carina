# Carina Latency / Snappiness / Hot-path Re-audit (post-0.8.38)

> Date: 2026-08-21  
> Carina: **v0.8.38** (`514f501`)  
> Role: Agent Harness Performance Architect. “The task finished” is not a pass.  
> Prior: **[20](./20-hot-path-latency-audit.md)** (v0.8.32 FAIL ~4.5/10); P0-H1–H7 shipped in **0.8.33**.  
> Law: `docs/HOT_PATH.md`.  
> Host: this checkout, aarch64 macOS. Model network **not** used except as a named code path.  
> **Verdict (as of v0.8.38 tag): PASS ~7/10 for chrome/first-frame; FAIL remaining on Grok process-per-Think and kernel-stdio serialization of capability-gated writes.** TUI first paint now matches jcode’s published TTFF band.
>
> **Follow-up:** **P0-H11 landed** after 0.8.38 — turn 2+ reuses the ACP/`stdio` child; inspect skip unchanged; Grok prompt-cache kind stays `none`. **P0-H12** (list/search memo) remains open. Do not claim Anthropic-style prefix cache on Grok `full()`.

---

## Phase 0 — Ruler (source, not README)

### jcode (`…/competitors/jcode`)

| Job | Magnitude / mechanism | Evidence |
|-----|------------------------|----------|
| TTFF **14.0 ms** (10.1–19.3) | README-published PTY harness; **not** a compile-time SLO | `README.md` ~187–223 |
| TTFI **48.7 ms** (30.3–62.7) | same | same |
| Paint before server | `startup_profile::mark("first_frame")` on first `draw_full` **then** `connect_with_retry` | `crates/jcode-tui/.../run_shell.rs` ~761–790 |
| Input vs bus | One TUI `tokio::select! { biased; event_stream first }` — **not** a second thread | `run_shell.rs` ~877–884 |
| Native `tool_calls` | **serial** `for tc in tool_calls` so interrupt can still write `tool_result` | `turn_loops.rs` ~841–868 |
| Real parallel tools | `batch` tool, `MAX_PARALLEL = 10`, `FuturesUnordered` | `tool/batch.rs` |
| Stream keepalive | `ServerEvent::Pong` every **30s** while opening/reading | `streaming.rs`, `turn_streaming_mpsc.rs` |
| Swarm return | completion report **≤4000 chars**, TL;DR ≤200; **not** worker transcript | `jcode-swarm-core` |
| Telemetry | `first_tool_call_ms` / `tool_latency_*` instrumented; README `900/1800` are **schema examples** | `TELEMETRY.md` |
| Per-session RAM | README-only PSS (~10 MB extra / session) | `README.md` |

Steal: **paint before the daemon is interesting**, **batch that is actually concurrent**, **Pong**, **biased input**. Do not steal “in-turn native parallel tool_calls” — jcode serializes those on purpose. Do not copy jemalloc.

### Grok Build (`…/competitors/grok-build`, `SOURCE_REV` `8d69c91f`)

| Job | Mechanism | Evidence |
|-----|-----------|----------|
| Event-driven redraw | Dirty presenter; idle parks until an event | `xai-grok-pager/.../event_loop.rs` ~320–407 |
| 16ms throttle | `request_throttled`; env `GROK_MIN_DRAW_MS` | same ~375–384; `display_refresh.rs` ~11–12 |
| Dedicated input thread | OS `poll+read` → mpsc; ACP stdin is a separate blocking reader | `event_loop.rs` ~1446–1521 |
| Biased `select!` | Cancel/quit > writer > **ACP gated on empty input** > tasks > input | same ~2050–2090 |
| ACP drain batch | After first recv, `try_recv` up to **32** then one throttled draw | same ~1802–2126 |
| Parallel subagents | OS thread per child, 8MiB stack, own current-thread tokio | `xai-grok-shell/.../spawn.rs` ~2151–2287 |

Steal: **input cannot lose to the token hose**; **drain ACP in batches**. Do not copy Grok pager pixels or `--yolo`.

### Oh My Pi (`…/competitors/oh-my-pi`)

| Job | Mechanism | Evidence |
|-----|-----------|----------|
| In-process ripgrep/glob/find | N-API `pi-natives` + shell builtin `pi-uu-grep`; fork/exec of `rg` is a **product defect** | `crates/pi-natives/src/grep.rs`; `crates/pi-uu-grep`; `README.md` ~170, 424 |
| Hashline edits | Default `edit.mode = hashline` | `docs/tools/edit.md`; `packages/hashline/src/apply.ts` |
| Task concurrency | Session semaphore, **default 32** (`0` = unlimited) | `packages/coding-agent/src/task/index.ts` ~576–637 |
| Render | Event-driven `requestRender`; idle has **no** timer | `packages/tui/src/tui.ts` ~1921–1953 |

Steal: **do not fork for a 4ms grep if you can avoid it**. Do not copy N-API / hashline monopoly / default yolo.

### Claude Code (notes)

| Job | Mechanism | Evidence |
|-----|-----------|----------|
| Prompt cache = longest common prefix | Static prefix first | `claude-code-notes/04-Agent协调/06-Fork与提示词缓存优化.md` |
| `defer_loading` tools | Tool list must not bust cache mid-run | `INTERVIEW_QA.md`; `02-工具系统/05-MCP类工具.md` |
| Parallel `tool_use` | Stream-start; consecutive `isConcurrencySafe()` run together; Edit starts a new partition | `04-Agent协调/04-QueryEngine.md` ~481–524 |
| Subagent **summary only** | `finalizeAgentTool()` truncates; parent does not keep child transcript | `02-工具系统/04-Agent任务类工具.md` ~96–160 |

Carina already: Anthropic `cache_control` A–D; MCP index + `mcp_find`; subagent summary-only. Grok/OpenAI cache kind **`none`** is honest.

### Codex (`…/competitors/codex`)

| Job | Mechanism | Evidence |
|-----|-----------|----------|
| `parallel_tool_calls` | Model flag on Responses API | `codex-rs/core/src/session/turn.rs` ~1278–1287 |
| Parallel vs serial tools | Read lock = many safe tools; write lock = exclusive unsafe | `core/src/tools/parallel.rs` ~41–60 |
| Sandbox orchestrator | Async; `SandboxManager` is a **stateless** unit struct | `tools/orchestrator.rs`; `sandboxing/src/manager.rs` |
| AGENTS.md | Walk root→cwd; byte budget; `---` separators | `core/src/agents_md.rs` |
| Deferred tools | `tool.defer_loading` → name-only until search restores schema | `core/src/tools/handlers/dynamic.rs` ~59–80 |

Steal: **async capability path**. Do not drop Carina’s fail-closed kernel to win a microbench.

---

## Phase 1 — Carina decomposition (numbers, this checkout)

| Stage | Magnitude (2026-08-21) | On critical path? | How measured |
|-------|------------------------|-------------------|--------------|
| PTY TTFF | **15.2 ms** (p50) | first byte of conversation chrome | `cargo run -p carina-tui --bin tui_bench` `pty_first_frame` |
| PTY TTFI | **15.4 ms** (p50) | composer on screen | same (`ttfi_ms`) |
| Offscreen markdown | p50 **36.7 ms** / p95 38.9 / p99 43.5 | fat transcript paint | `tui_bench --iterations 20` synthetic |
| Startup inventory | **after** first paint; 20×250ms poll if grok-build not ready | **no** (async thread) | `queue_startup_inventory_refresh` `app/mod.rs` ~1285 |
| Constitution A–D | **~3.0 kB / 792 tok** (`len/4`, Fixture G/R < 800) | every run; Anthropic cached | `TestConstitutionWithoutWorkspaceStaysUnder800Tokens`; `coreConstitution()` |
| Native envelope | **167 B** | native HTTP only | `nativeToolsContract` |
| Layers compose | **once per run** | task start | `composeAgentPromptLayers` |
| `seg.full()` | string concat **every turn** | every Think | `agent.go` ~400 |
| Prompt cache | Anthropic `cache_control` A–D (cap 4); Grok/OpenAI **`none`** | Grok default | `promptCacheKindFor` |
| Grok isolation | **reuse `grokHome`**; `inspect` skipped after first verify; **ACP/`stdio` process persisted across Thinks (P0-H11)** | first Think of a Grok reasoner: start+preflight; later Thinks: `session/prompt` on the same pipes | `grok_reasoner.go` `acquireACP` / `startACP` |
| Compaction | `compact(nil)` cheap elide; model summary **after** the turn | cheap yes; summarizer **no** | `agent.go` ~381–385; `summarizeTranscriptAfterTurn` |
| Rebuild after compact | kernel FileRead ≤5 cited files, 8k cap, volatile | after fold, before next Think | `compact_rebuild.go` |
| `list` (bounded) | **warm 0–20 ms** / cold first exec ~20–650 ms; `--max-files 200 --max-depth 4` | yes, still **fork** `carina-scan` | this repo, 201 JSONL lines |
| `list` (unbounded, not shipped) | warm **40–160 ms**, 1433 files | not on path | `carina-scan .` |
| `search` (`go/daemon`) | warm **40–60 ms** | yes, **fork** `carina-grep` | `carina-grep 'package daemon' go/daemon` |
| Observation shape | search: files×why; list: dirs+samples (not 50/200 raw lines) | next model turn | `tool_extract.go` |
| Parallel batch | one workspace `FileRead`, then goroutines; per-file kernel **skipped** via `authorizedRead` | IO **can** overlap | `executeBatch` `agent.go` ~1211–1237 |
| Kernel pipe | `rpc.Client.Call` **holds `mu` for the whole stdio round-trip** | every `kern.Request` (run/patch/spawn/unbatched read) | `go/rpc/client.go` ~144–146 |
| Subagent spawn | new session + `InitSessionFull` + **git worktree if not read-only/sandboxed** | parent `wg.Wait()` | `subagent.go` ~45–60, 139–150, 409–415 |
| Subagent return | **`done.summary` only** | parent blocked until child `done` | comments + `runSubagentLoop` |
| Workflow ready set | independent steps `go` in parallel; kernel still serializes capability RPC | yes | `workflow.go` ~168–176 |
| Stream → UI | dedicated `terminal-input` thread; `assistant.message.delta`; 16ms present | paint async; **TTFT still waits on Think** | `app/mod.rs` ~9066–9084 |
| Keepalive | bus-only `execution.keepalive` after 1s during Think and tool waits | long wait no longer silent | `keepalive.go`; `startThinkKeepalive` |
| MCP | names at layer compose; full schema via `mcp_find` | run start, not every turn | `promptcache.go` |

Evidence log: local `tui_bench` + `/usr/bin/time -p` on `zig/zig-out/bin/carina-{scan,grep}` (warmed ×3).

### Forced answers

1. **TTFT (chrome)** is **15 ms**. **TTFT (first model token)** is model-bound **plus**, on Grok, a **new CLI process every Think** even after inspect skip.
2. **Context assembly** is once/run for layers. `full()` rebuilds the stuffed string every turn. Anthropic can cache A–D; Grok cannot honestly claim prefix cache.
3. **list/search wall-clock** is no longer a 0.55s tree walk. Warmed bounded scan is **≤20 ms**. The remaining tax is **fork/exec**, not walking `target/`.
4. **Parallel list/read/search** is real goroutine + overlapping IO for reads. It is **not** parallel `kern.Request`. Writes/spawn still queue on the kernel mutex.
5. **Subagent** is summary-only (pass) and **session-heavy** (fail vs in-process worker).
6. **Compaction** does not sit in front of Think with a model call (pass vs 0.8.32).
7. **Keepalive** exists (pass vs 0.8.32).
8. **Input** is a dedicated thread (pass).

---

## Phase 2 — Broken Performance Patterns

| # | Pattern | 0.8.32 | 0.8.38 |
|---|---------|--------|--------|
| 1 | Parallel tools actually serial | **FAIL** (kernel mu on every FileRead) | **partial**: batch FileRead once; writes/spawn still mutexed |
| 2 | Full system+tools+history restuff | **FAIL** (`toolsHelp` 4.8kB) | **partial**: A–D < 800 tok; `full()` still concat; Grok `none` |
| 3 | Slow tool / compact blocks event loop | **FAIL** (summarizer on Think) | **PASS** cheap compact; tools keepalive |
| 4 | Heavy subagent | **partial** | **partial**: summary-only; still new session+kernel+optional worktree |
| 5 | Wait for whole stream to paint | **partial** | **PASS** deltas + 15ms first frame |
| 6 | MCP rediscover every turn | **PASS** already | **PASS** |
| 7 | No stream keepalive | **FAIL** | **PASS** `execution.keepalive` |
| 8 | Input starved | **PASS** | **PASS** `terminal-input` thread |
| 9 | Full-tree grep/glob | **FAIL** (1400-file list) | **partial**: bounded scan; **no** intra-run memo |
| 10 | Heavy JSON on hot path | **FAIL** | **FAIL** every kernel round-trip still JSON+hash |
| 11 | Swarm named-parallel actually serial | **partial** | **partial**: goroutines yes; kernel pipe no |
| 12 | Fork/exec for ls/grep | **FAIL** | **FAIL** (deliberate Zig tools; warmed 0–60 ms) |
| 13 | Huge tool obs back into next turn | **partial** snip | **PASS** snip 2k + search/list extract |
| 14 | Sync prep before spawn | **FAIL** inspect every Grok turn | **partial**: inspect skip; **still exec CLI** |

---

## Phase 3 — Scorecard

| 热路径 | 参考量级 | Carina 0.8.38 | 分数 | 主要 Pattern | 优先级 |
|--------|----------|---------------|------|--------------|--------|
| TTFF / first input | jcode 14 / 49 ms | **15.2 / 15.4 ms** PTY | **9** | — | — |
| Context assembly | Claude prefix cache | A–D <800 tok; Anthropic cached; Grok `full()` none | **7** | 2, 10 | P1 (Grok process, not more prose) |
| Single tool round-trip | OMP in-process rg | zig fork **40–60 ms** grep; kernel JSON extra | **6** | 12, 10 | P1 memo; do not drop kernel |
| Parallel tools | jcode `batch`+FuturesUnordered | list/read/search goroutines + one FileRead | **7** | 1 | — |
| Subagent spawn + return | Claude summary-only | summary-only **PASS**; InitSession+worktree **tax** | **6** | 4, 14 | P1 |
| Swarm / orchestration | Grok thread fan-out | goroutine fan-out; kernel serializes caps | **6** | 11 | P1 |
| Stream → UI paint | Grok 16ms + biased select | 16ms present + input thread + deltas | **8** | — | — |
| Compaction blocking Think | — | `compact(nil)` then Think; summary after | **8** | — | — |
| Grok TTFT extra | — | **P0-H11 landed**: turn 2+ does not `exec` a new `grok`; Grok cache kind stays `none` | **7** | 14 | — |

**总体：~7/10.** Chrome is no longer the excuse. Grok process-per-turn and kernel-stdio writes are.

---

## Phase 4 — Knives

### P0 (利索感, remaining)

| ID | Problem | Evidence | Steal | Change | Accept |
|----|---------|----------|-------|--------|--------|
| **P0-H11** | Grok `Think` still `exec`s a new CLI every turn | **landed**: persist one ACP/`stdio` child on the reasoner; Think timeout binds the in-flight prompt; Close/failed Think kills the child | jcode keeps a server; Grok pager keeps ACP | Persist one ACP process for the **run**, reuse `grokHome` **and** the child | Turn 2+ of a Grok run does **not** start a new OS process; do **not** claim Grok prefix cache |
| **P0-H12** | Repeat `list`/`search` in one run re-forks | warmed scan 0–20 ms still a process | OMP in-process is the job, not the crate | Memo last bounded scan / last grep `(root, pattern)` for the run; invalidate on patch/edit | Second `list` in the same run does not spawn `carina-scan` |

Do **not** make P0: drop hash-chain, in-process rewrite of zig grep, Grok prefix-cache lie, MiniLM.

### Already landed (do not reopen)

P0-H1 inspect skip; P0-H2 batch FileRead; P0-H3 bounded list; P0-H4 PTY TTFF + paint before inventory; P0-H6 compact off Think; P0-H7 keepalive; **P0-H11 persist Grok ACP stdio**; constitution <800; search/list extract.

### P1

| ID | Item |
|----|------|
| P1-H1 | `first_tool_call_ms` / `tool_latency_max_ms` on the session (RoutingOutcome already has `latency_ms`) |
| P1-H2 | Kernel request pipelining **without** dropping the mutex invariant (batch FileRead/FileWrite envelopes) |
| P1-H3 | Subagent: skip git worktree for more profiles; reuse kernel session slots |
| P1-H4 | MCP schema bytes stay behind `mcp_find` (already); cache `mcp_find` hits per run |
| P1-H5 | Orchestration wall-clock = max(child) in traces, not sum |

### P2

Filesystem scan cache across runs; tokenizer-accurate pressure; EWMA compact (already deferred). Swarm result merge visualization.

---

## Anti-patterns

1. Do not treat goroutine fan-out as parallel if the **capability** round-trip is still one stdio mutex — say it.
2. Do not `grok inspect` every JSON action (fixed); do not **re-exec grok** every JSON action either.
3. Do not walk `target/` (fixed 0.8.32).
4. Do not enable MiniLM / snapcompact / wholesale Grok pager to “feel faster”.
5. Do not drop hash-chained audit to win a microbench.
6. Do not load every MCP schema to avoid one `mcp_find`.
7. Do not copy child transcripts into the parent.
8. Do not busy-spin the UI loop (keep event-driven 16ms).
9. Do not claim Anthropic-style prefix cache on Grok ACP `full()`.
10. Do not treat “we have spawn” as snappy if spawn always `InitSessionFull` + worktree.
