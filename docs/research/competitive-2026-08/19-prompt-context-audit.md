# Carina Prompt / Context Engineering Audit

> Date: 2026-08-20
> Carina: **v0.8.30** audit. P0 knives S1–S6 shipped in **v0.8.31**.
> Role: Context Engineering Architect. “Agent can reply” is not a pass.
> **Verdict at audit: FAIL.** Default **build** plus dumped `AGENTS.md` plus
> “batch all independent search on first exploration” sent a two-word
> operator message into a 46-tool repo crawl. Keep this file as the
> evidence; live law is `docs/PROMPT_SPEC.md`.

This file is the live prompt/context SSOT for the audit. Spec law:
`docs/PROMPT_SPEC.md` and `docs/CONTEXT_ENGINEERING.md`.

---

## Phase 0 — Ruler (from source, not marketing)

| Job | Claude Code | OMP | Grok Build | Codex | jcode |
|-----|-------------|-----|------------|-------|-------|
| Prompt shape | `string[]` + `__DYNAMIC_BOUNDARY__` | `.md` modules + Handlebars → `string[]` | MiniJinja `prompt.md` + agent MD | model `.md` + context fragments | embedded `.md` + `SplitSystemPrompt` |
| Cache | static global ephemeral; CLAUDE.md **after** boundary | soft (second system msg / snapcompact) | stable system; volatile in `<system-reminder>` | fragments re-injected post-compact | explicit static vs dynamic |
| Rules | CLAUDE.md cwd↑ + `~/.claude`; deeper wins | native/claude/agents priority shadow | AGENTS/Claude deeper-wins; **verbatim re-inject after compact** | AGENTS root→cwd + byte cap | project + `~/AGENTS` into **static** |
| Tools | lazy Zod; builtins before MCP | arktype + inventory in prompt | schemars; registry | ToolSpec JSON schema | sorted schemas + forced `intent` |
| Compact | snip → microcompact → collapse XOR autocompact (~167k/200k) | threshold/overflow/idle; snapcompact default | ~85% full-replace; 7-section summary | token-limit + remote; handoff | 80% + **proactive EWMA** + **semantic** |
| Memory | CLAUDE.md + memdir; 200 lines / 25k B | session-start ≤5k tok | first-turn search; freeze block to protect KV | 2500 tok summary + progressive files | async N→N+1 into **dynamic** |
| Subagent | workers: no parent chat; fork: shared prefix | isolated pi process | own prompt; compacted AGENTS optional | child context modules | swarm overlay in dynamic |

**Ruler (must all be true):**

1. Constitution is short, stable, and cacheable without TASK or repo docs.
2. Conversation vs implementation is a **first-line** mode, not a buried DO NOT.
3. Project rules load when the turn needs repo work, not on every “hi”.
4. Tools: builtins stable; MCP/skills on demand.
5. Compaction is a cascade, then **re-injects** rules/memory that must survive.
6. Subagents return summaries; parent does not eat child transcripts.
7. Negative instructions are few, ranked, and do not fight the first line.

DeepSeek: no local clone. Public notes say SystemPrompt-as-plugin; not scored.

---

## Phase 1 — Carina inventory (files, not slogans)

### Assembly

```
go/daemon/agent.go          productIdentity + productCapabilityBrief + toolsHelp
                            = const systemPrompt  (monolithic)
go/daemon/agents.go         builtin "build" prepends
                            "Inspect the workspace, make targeted changes…"
go/daemon/promptcache.go    composeAgentPromptLayers once per run
                            Constitution → Workspace → Catalog → TASK trailer
go/daemon/agent.go:358      every turn: compact? then seg.full() rebuild
```

Final string: `StablePrefix + VolatileSuffix`. Hot path **rebuilds the string
every turn** from frozen layers + `tr.render()`.

### Cache

`CacheSections()` exists. Anthropic Messages may attach `cache_control`.
`promptCacheKindFor`: Grok Build / Claude CLI / OpenAI-compat = **`"none"`**.
Grok still gets `seg.full()` stuffed as **user** text after:

```
Act only as a pure inference engine. Do not call tools…
Carina inference request follows as plain text.
```

(`go/daemon/grok_reasoner.go` `grokACPSystemPrompt`, `grokACPPromptPrefix`)

TASK trailer is **inside** `StablePrefix`. Per-run cache yes; cross-task no.
Catalog includes skill **index** selected from `task.UserPrompt` — a greeting
that happens to match a trigger mutates the “stable” catalog.

### Tools

JSON ReAct: **entire `toolsHelp` in constitution every turn** (list through
spawn/workflow/mcp/done). Native tools (`tool_schema.go` `carinaToolSpecs()`):
full catalog on first attempt if HTTP + ToolCall and **not** Grok Build.
MCP: names in catalog; schemas via `mcp_find`. Skills: bodies via `skill://`.

### Compaction

`Transcript.compact` every turn (`transcript.go`). Trigger ~80% of
(window−reserve) (`compaction_budget.go`). Cascade: snip on enqueue
(`ToolOutputMax` 2000) → elide old unpinned → collapse_only until ~1.10× →
model summary. `go/contextengine` is **noop**. No CLAUDE.md-style rebuild of
files after compact (layers already frozen). No proactive/semantic.

### Persistent instructions

`memory.go` `loadMemory`: `~/.carina/CARINA.md` then root→cwd first of
CARINA.override / CARINA.md / AGENTS.override / AGENTS.md. **8000 char dump**
into Workspace as `PROJECT INSTRUCTIONS (… follow them)`. Explore subagent
skips this. Compact does **not** drop it (frozen). Greeting **does** get it.

### Memory

`d.memory.snapshot` frozen at task start. HMS optional, pinned observation
not system prompt. Writes mid-run do not refresh the snapshot.

### Subagent

`subagent.go`: own session, own layers, **only `done.summary` returns**.
Explore uses lean `exploreToolsHelp`. This part **passes**.

### Tokens / UI

`estimateTokens = len/4+1`. `context.summary` warning 80 / critical 90.
TUI `/context` exists. In-loop compact ≠ idle `/compact`. Estimator is
not a tokenizer.

### The “hi” / `grok models` crawl (operator evidence)

Default agent is **build**. First sentence: inspect the workspace.
Greeting rule exists **later** in `toolsHelp`. Then 8k of this repo’s
`AGENTS.md`. Then: “On the first exploration turn, batch all independent
list/read/search”. Grok sees that blob as user data under “do not call
tools”. Result: ×46 Explore on `grok, model.?id`. Test
`conversation_first_test.go` only passes with a stub reasoner that
pattern-matches the buried sentence.

---

## Phase 2 — Broken patterns (one hit = FAIL)

| # | Pattern | Carina | Evidence |
|---|---------|--------|----------|
| 1 | Monolithic system prompt | **FAIL** | `const systemPrompt` concatenates identity+brief+tools+orchestration |
| 2 | No cache-aware split | **FAIL** | TASK in prefix; Grok/OpenAI `cache kind none`; catalog depends on user text |
| 3 | All tool schemas every turn | **FAIL** | `toolsHelp` + native `carinaToolSpecs()` full set; MCP index is the only lazy path |
| 4 | Raw tool results | **PARTIAL** | snip 2k + elide; still no structured extract; 46 searches still enter the model view |
| 5 | Only fire-at-cap compact | **FAIL** | 80% then collapse/summary; no proactive/semantic; contextengine noop |
| 6 | Compact drops rules | **PARTIAL** | frozen layers survive; not re-read; dumped even when unused |
| 7 | No token pressure | **PARTIAL** | `/context` + estimates exist; estimator is `len/4`; Grok path opaque |
| 8 | Subagent transcript dump | **PASS** | summary-only return |
| 9 | Adjective pile / no rank | **FAIL** | greeting vs “inspect workspace” vs “batch search” vs capability brief |
| 10 | Role pollution | **FAIL** | Grok: system = pure inference; user = full Carina constitution |
| 11 | Hot-path full rebuild | **PARTIAL** | layers frozen; `full()` string rebuilt every turn |
| 12 | Memory no budget | **PARTIAL** | snapshot + 8k project docs; no retrieval budget for “hi” |
| 13 | Spec mixed with examples | **FAIL** | JSON tool examples live inside constitution |
| 14 | Excess DO NOT | **FAIL** | toolsHelp is a wall of never/do-not; first line still says inspect |

**Shipped prompt system: 1 PASS, 4 PARTIAL, 9 FAIL.**

---

## Phase 3 — Scores

| Dimension | Best reference | Carina | Score | Pattern | Priority |
|-----------|----------------|--------|-------|---------|----------|
| Prompt structure & cache | Claude boundary; jcode SplitSystemPrompt | One const + three layers; Grok no cache | **3** | 1, 2, 10 | **P0** |
| Instruction hierarchy | Grok/OMP deeper-wins, documented | build prepend vs buried greeting; AGENTS always-on | **3** | 9, 13 | **P0** |
| Tool loading | Claude lazy schema; MCP find | Full toolsHelp every turn | **4** | 3 | **P0** |
| Compaction quality | Claude 4-tier; jcode proactive | 80% elide/summary | **5** | 5 | P1 |
| Persistent-instruction lifecycle | Grok verbatim re-inject | 8k dump at start | **4** | 6, 12 | **P0** |
| Memory inject | OMP 5k / Codex progressive | frozen snapshot + optional HMS | **5** | 12 | P1 |
| Subagent isolation | Claude workers; Carina summary-only | summary-only | **8** | — | keep |
| Token observability | Claude window math | len/4 + /context | **5** | 7 | P1 |

**Weighted product score: ~4 / 10. FAIL.**

Gap in one sentence: Carina already knows how to say “greetings → done”.
The **first line of the default agent** and the **repo instruction dump**
tell the model the opposite.

---

## Phase 4 — Deliverables

- Law: `docs/PROMPT_SPEC.md`, `docs/CONTEXT_ENGINEERING.md`
- P0/P1/P2 and anti-patterns live in those files
- This audit stays the evidence card

Do not start semantic compaction or a theme catalog for prompts until
Fixture G (greeting in a repo with AGENTS.md) returns `done` with no tools.
