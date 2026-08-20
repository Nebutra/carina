# Carina Latency / Snappiness / Hot-path Audit

> Date: 2026-08-20
> Carina: **v0.8.32**
> Role: Agent Harness Performance Architect. “The task finished” is not a pass.
> **Verdict: FAIL ~4.5/10.** TUI already has a dedicated input thread and 16ms
> event-driven redraw. Subagents already return summaries. The product still
> feels slow because **Grok inspects isolation every turn**, **kernel stdio is
> mutex-serialized under “parallel” tools**, **`list` walks the whole tree**,
> and **compaction can call the model on the ReAct stack**.
>
> Law: `docs/HOT_PATH.md`.

---

## Phase 0 — Ruler (from source, not marketing)

Measured/declared magnitudes in **competitor trees on this machine**.

### jcode (`…/competitors/jcode`)

| Job | Evidence |
|-----|----------|
| TTFF **14.0 ms** (10.1–19.3) | `README.md` + PTY harness `scripts/bench_startup_visible_ready.py` |
| TTFI **48.7 ms** (30.3–62.7) | same; probe text on `pyte` screen |
| First paint mark | `crates/jcode-tui/.../run_shell.rs` `startup_profile::mark("first_frame")` — first `draw_full` **before** the server is interesting |
| Telemetry (turn, not TTFF) | `TELEMETRY.md`: `first_tool_call_ms` 900, `tool_latency_max_ms` 1800 |
| Native tool_calls in one model turn | **serial** (`run_turn` / `run_turn_streaming_mpsc` for-loop) — intentional, so interrupt can still write `tool_result` |
| Real parallel tools | `batch` tool, `MAX_PARALLEL = 10`, `FuturesUnordered` (`tool/batch.rs`) |
| Stream keepalive | `ServerEvent::Pong` every **30s** while opening and reading (`streaming.rs`, `turn_streaming_mpsc.rs`) |
| Swarm return | completion report **≤4000 chars**, TL;DR ≤200; **not** worker transcript (`jcode-swarm-core`) |
| Event loop | `tokio::select! { biased; event_stream.next() first }` so the bus cannot starve keys |

They **publish** first-frame numbers. Carina does not. Do not steal “in-turn
native parallel tools” from jcode — they do not have that. Steal **batch that
is actually concurrent**, **Pong keepalive**, and **paint before the daemon
matters**.

### Grok Build (`…/competitors/grok-build`)

| Job | Evidence |
|-----|----------|
| 16ms redraw cadence | `xai-grok-pager/src/app/event_loop.rs` ~1800, 1966, 2270 |
| `biased` `select!` so ACP firehose cannot starve quit/input | same file ~2051–2086; ACP arm **gated on empty terminal input** |
| Resize debounce 16ms | same file ~1798 |
| ACP drain batch 32 | same file ~1807 |
| Parallel subagents on dedicated threads | `xai-grok-pager/src/acp/spawn.rs` ~247 |

Wall-clock of a fan-out is the slowest child **if** children do not share a
mutexed pipe. Carina’s spawn goroutines are real; its kernel pipe is not.
jcode’s analog of `executeBatch` is `batch`+`FuturesUnordered`, not a locked
stdio client.

### Oh My Pi (`…/competitors/oh-my-pi`)

| Job | Evidence |
|-----|----------|
| In-process ripgrep/glob/find | `README.md` ~170, 424; `crates/pi-uu-grep` |
| Hashline edits (stale-anchor recovery) | `README.md` ~236; `packages/hashline` |
| Worker pool cap default 8 | `python/robomp/AGENTS.md` `ROBOMP_MAX_CONCURRENCY` |

They treat **fork/exec of rg/find as a product defect**. Carina `list`/`search`
still spawn `carina-scan` / `carina-grep`.

### Claude Code (notes, not a local clone)

| Job | Evidence |
|-----|----------|
| Prompt cache = longest common prefix | `claude-code-notes/04-Agent协调/06-Fork与提示词缓存优化.md` |
| Deferred tools so the tool list does not bust cache | `INTERVIEW_QA.md` ~903; `02-工具系统/05-MCP类工具.md` |
| Fork subagents share parent prefix; child does not dump transcript back | fork notes; AgentTool summary contract in `04-Agent协调/02-tasks.md` |

Carina: Anthropic `cache_control` exists; Grok/OpenAI kind is `"none"`.
`toolsHelp` (~4.8kB) is still in constitution every run. MCP schemas are
already demand-gated (`mcp_find`). Subagent summary-only **matches**.

### Codex (`…/competitors/codex`)

| Job | Evidence |
|-----|----------|
| `parallel_tool_calls` is a first-class model/tool flag | `codex-rs/core/src/compact_remote_request.rs`; `tools/handlers/mcp.rs` `supports_parallel_tool_calls` |
| Sandbox orchestrator is async, not a stdio mutex | `tools/orchestrator.rs` |
| Light explorer agent | `core/src/agent/` builtins |

Codex parallel tools are **model-native multi-tool** plus async runtimes.
Carina parallel is a JSON `actions:[]` batch of three verbs, then Go
goroutines over a **locked** kernel client.

---

## Phase 1 — Carina decomposition (numbers)

Host: this checkout, 2026-08-20, aarch64 macOS. Model network **not** used
for these numbers (except where noted as “code path only”).

| Stage | Magnitude | On critical path? | How measured |
|-------|-----------|-------------------|--------------|
| Offscreen markdown render | **p50 37ms / p95 38ms / p99 46ms** | first paint of a fat transcript | `cargo run -p carina-tui --bin tui_bench -- --iterations 40` (explicitly *not* PTY TTFF) |
| PTY TTFF / TTFI | **unmeasured** | yes | no Carina PTY bench; jcode publishes 14ms / 49ms |
| Startup inventory | up to **20 × 250ms** if grok-build is not immediately runnable | after first loop, async thread | `queue_startup_inventory_refresh` |
| Constitution size | **~8.3kB / ~2.1k tok** before mode prepend (`toolsHelp` 4806B alone) | every run, cached only on Anthropic | const lengths in `agent.go` |
| Layers compose | **once per run** | task start | `composeAgentPromptLayers` |
| `seg.full()` | string concat **every turn** | every Think | `agent.go` ~390–397 |
| Prompt cache | Anthropic `cache_control`; Grok/OpenAI **`none`** | Grok default in this product | `promptCacheKindFor` |
| Grok isolation | **`newIsolation` + `grok inspect --json` every `ThinkRoutedModel`** | before first token of **every** turn | `grok_reasoner.go` ~160–183, 799 |
| Compaction | `shouldCompact` cheap; **summarizer = extra model call on the loop** | when over budget, before Think | `agent.go` ~336–346, 380 |
| `list` | **0.15s** scan-to-file / **0.55s** to `/dev/null` on this repo (1405 files), then UI keeps **200** lines | yes | `carina-scan .`; `agent.go` list loop `i >= 200` |
| `search` (`go/daemon`) | **80ms** | yes | `carina-grep 'package daemon' go/daemon` |
| Parallel batch | goroutines **yes**; kernel `Client.Call` **`mu.Lock` whole round-trip** | yes | `executeBatch`; `go/rpc/client.go` ~144–147 |
| Kernel | child process stdio JSON-RPC + hash chain per `kernel.request` | every tool | `go/kernel/kernel.go` |
| Subagent spawn | new session + `InitSessionFull` + **git worktree if not read-only** | parent `wg.Wait()` | `subagent.go` 139–204, `spawnUsesWorktree` |
| Subagent return | **summary only** | parent blocked until child `done` | comments + `runSubagentLoop` |
| Stream → UI | dedicated input thread + `assistant.message.delta` + 16ms present | paint is async; **TTFT still waits on Think** | `app/mod.rs` `run` |
| Keepalive | **none** on kernel/tool; run-active ticks 33/80ms | long tool looks idle except spinner | — |
| MCP | tool **index** at layer compose; full schema via `mcp_find` | run start, not every turn | `promptcache.go` |

### Forced answers

1. **TTFT** is model-bound **plus** Grok inspect (H1). Not “the network”.
2. **Context assembly** is once/run for layers, but `full()` + uncached Grok
   blob every turn (H7/H8).
3. **Single tool** is kernel RPC + optional zig fork + audit (H3/H4/H6).
4. **Parallel tools** are concurrent Go, serial kernel (H2). **Pattern 1.**
5. **Subagent** is a real session, not a lightweight worker; return contract
   is already summary-only.
6. **UI** is not waiting for the whole assistant message, but it **is**
   waiting for isolation+model before the first delta.

---

## Phase 2 — Broken Performance Patterns

| # | Pattern | Carina |
|---|---------|--------|
| 1 | Parallel tools actually serial | **FAIL** — goroutines + `rpc.Client.mu` |
| 2 | Full system+tools+history restuff, cache-hostile | **FAIL** on Grok/OpenAI (`cache: none`); **partial** Anthropic |
| 3 | Main loop blocked by slow IO / compaction | **FAIL** when summarizer fires; tool IO is daemon-side (OK) |
| 4 | Heavy subagent (process/session clone) | **FAIL** for write profiles (git worktree + kernel session); explore is lighter |
| 5 | Stream waits for whole message | **PASS** (deltas exist) |
| 6 | MCP/tools rediscover every turn | **PASS** for MCP schemas; **FAIL** for builtin `toolsHelp` bulk |
| 7 | No stream keepalive | **FAIL** |
| 8 | Input starved by token stream | **PASS** (input thread; Grok pager’s bug class already avoided) |
| 9 | FS walk without prune/cache | **partial** — 0.8.32 prunes ignore dirs; **no cache**; `list` still full-walks |
| 10 | Hot-path JSON/string rebuild | **FAIL** — `full()` + kernel JSON every tool |
| 11 | Swarm serial | **partial** — workflow ready-set is parallel; kernel still serializes |
| 12 | fork/exec for ls/grep | **FAIL** — zig tools |
| 13 | Huge tool results re-injected | **partial** — snip 2k / KeepRecent 3 |
| 14 | Sync prep before subagent/workflow | **FAIL** — Grok inspect; worktree create; kernel init |

Any one FAIL is a product FAIL. There are eight.

---

## Phase 3 — Score

| Hot path | Reference magnitude | Carina now | Score | Main pattern | Pri |
|----------|---------------------|------------|-------|--------------|-----|
| TTFF / first input | jcode 14ms / 49ms PTY | no PTY bench; markdown p50 37ms; inventory 250ms retries | 3 | H4, H10 | P0 |
| Context assembly | Claude static prefix + cache | layers once/run; Grok uncached stuffed blob; `toolsHelp` 1.2k tok | 5 | 2, 10 | P0 |
| Single tool | OMP in-process rg | zig fork + kernel stdio; list 150ms+ then truncate | 4 | 9, 12 | P0 |
| Parallel tools | Codex native parallel + async | goroutine fan-out, mutexed kernel | 4 | 1 | P0 |
| Subagent spawn+return | Claude summary; Grok wall-clock ≈ max | summary-only **PASS**; spawn = session+kernel+worktree | 5 | 4, 14 | P1 |
| Swarm / orchestration | jcode server swarm | workflow DAG parallel; parent blocked; kernel HOL | 5 | 11 | P1 |
| Stream → UI | Grok 16ms + biased input | 16ms + input thread **PASS**; first delta waits on Think | 7 | H1 | P0 |
| Compaction blocking | Claude microcompact off hot path | local elide OK; model summary on the ReAct stack | 4 | 3 | P0 |

**Overall: FAIL ~4.5/10.**

---

## Phase 4 — P0 / P1 / P2

### P0 (利索感)

| ID | Problem | Evidence | Steal | Change | Accept |
|----|---------|----------|-------|--------|--------|
| P0-H1 | Grok inspect every Think | `grok_reasoner.go` `ThinkRoutedModel` → `verifyPureInferenceSurface` | Grok pager does not re-`inspect` per token | Reuse isolation after first verify | Turn 2+ `RoutingOutcome.latency_ms` loses the inspect child |
| P0-H2 | “Parallel” reads serialize on kernel | `rpc.Client.Call` `mu.Lock`; `executeBatch` | Codex async tool runtimes | One FileRead gate per batch, then concurrent `os.ReadFile` / scan | 3-file batch wall-clock ≈ slowest file |
| P0-H3 | `list` walks 1400 files for 200 lines | `agent.go` list + `carina-scan` 0.15s | Claude `code.map` / bounded glob | Bounded list or `code.map` first | list observation < 50ms on this repo |
| P0-H4 | No PTY first-frame; inventory can delay | `tui_bench` notes; `queue_startup_inventory_refresh` | jcode TTFF bench | Paint conversation before inventory; add PTY TTFF/TTFI | Empty frame before grok inventory; bench number exists |
| P0-H5 | Motion after Enter waits on Think | Feedback budget 80ms exists; first delta waits isolation+model | Grok immediately ticks activity | Thinking/tool cell on `execution.start` | Enter → visible motion ≤ 80ms |
| P0-H6 | Compaction summarizer on the Think stack | `runLoopContext` compact then Think | Claude cheap-first; model compact off the first token | Elide/collapse sync; summary async | Over-budget turn still Thinks without a nested model call |

### P1

- OpenAI/Grok prefix-cache adapter (do not stuff constitution as unlabeled user data — already framed; still uncached).
- Shrink `toolsHelp`; native-tools path must not also paste the JSON catalog.
- Stream keepalive / tool heartbeat events on calls > 1s.
- `list`/`search` in-process (link ripgrep or reuse pruned zig as a library).
- Filesystem scan cache keyed by `(root, mtime generation)`.
- Worktree spawn only when the child will write (already for explore).

### P2

- Swarm wall-clock dashboard (ready / running / blocked).
- Kernel request pipelining (multi-in-flight stdio) **without** breaking hash-chain order.
- MCP `defer_loading` equivalent: do not even index 100 MCP blurbs until `mcp_find`.

---

## Anti-patterns (do not)

1. Do not ship MiniLM or default snapcompact to fake speed.
2. Do not clone Grok pager / jcode mermaid to win a frame benchmark.
3. Do not drop the kernel or audit hash chain.
4. Do not call goroutines “parallel tools” while `Client.mu` is held.
5. Do not `grok inspect` per JSON action.
6. Do not dump child transcripts into the parent.
7. Do not full-walk `target/` (0.8.32); add a test if you touch scan again.
8. Do not block first token on a compaction summary.

---

## What this audit did *not* measure

- Live Grok/Anthropic TTFT (network). Code path before the socket is still in
  the table.
- PTY first frame of `carina` (needs a harness like jcode’s).
- Kernel `request` microseconds under load (stdio child; serialized by
  construction).
