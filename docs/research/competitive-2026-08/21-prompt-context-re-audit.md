# Carina Prompt / Context Engineering Re-Audit (v0.8.33)

> Date: 2026-08-20
> Carina: **v0.8.33** source (`02b359f`).
> Prior evidence: `19-prompt-context-audit.md` (v0.8.30 FAIL; S1–S6 shipped 0.8.31).
> Law: `docs/PROMPT_SPEC.md`, `docs/CONTEXT_ENGINEERING.md`.
> Role: Context Engineering Architect. “Agent can reply” is not a pass.
>
> **Verdict: still FAIL ~5.5/10 as a prompt system.** The greeting crawl
> (hi → Explore×46) is no longer the first-line bug. The constitution is
> still one concatenated Go string (~8.5 kB / ~2.1 k tok) with the full
> tool catalog every turn. Grok/OpenAI still have cache kind `none`.

---

## Phase 0 — Ruler (source, not marketing)

Read this round: Claude Code notes `06/09-system-prompt工程.md`,
`06/10-上下文压缩.md`; OMP `packages/coding-agent/src/prompts/system/system-prompt.md`;
Grok `xai-grok-compaction/.../assemble.rs`; jcode `jcode-base/src/prompt.rs`
`SplitSystemPrompt`; Codex `docs/agents_md.md` + repo AGENTS. DeepSeek: no clone.

| Job | Claude Code | OMP | Grok Build | Codex | jcode |
|-----|-------------|-----|------------|-------|-------|
| Shape | `string[]` + `__DYNAMIC_BOUNDARY__` | Handlebars modules → XML-tagged sections | MiniJinja + agent MD | model `.md` + fragments | `SplitSystemPrompt { static, dynamic }` |
| Cache | static global ephemeral; CLAUDE.md **after** boundary (often as user) | soft; rules inject on miss, survive compact | stable system; volatile in `<system-reminder>` | fragments re-injected post-compact | explicit static vs dynamic |
| Rules | CLAUDE.md cwd↑ + `~/.claude`; deeper wins | native/claude/agents priority | AGENTS/Claude deeper-wins; **verbatim re-inject after compact** | AGENTS root→cwd + byte cap | project + `~/AGENTS` into **static** |
| Tools | lazy Zod; builtins before MCP | inventory in prompt; skills as `skill://` | schemars registry | ToolSpec JSON | sorted schemas + forced `intent` |
| Compact | snip → image → collapse XOR autocompact (~167k/200k) then **rebuild files** | threshold/overflow/idle; snapcompact **optional** | ~85% full-replace; assemble `[SP, UP', AGENTS?, UQ, recent, summary]` | token-limit + remote handoff | 80% + **proactive EWMA** + **semantic** |
| Memory | CLAUDE.md + memdir caps | session-start ≤5k tok | first-turn search; freeze block | 2500 tok summary + progressive files | async N→N+1 into **dynamic** |
| Subagent | workers: no parent chat; fork: shared prefix | isolated process | own prompt; compacted AGENTS optional | child context modules | swarm overlay in dynamic |

**Ruler (all must be true):**

1. Constitution is short, named sections, cacheable without TASK/repo docs/tool examples.
2. Mode is the first operative sentence, not a later DO NOT.
3. Project rules load when the turn needs repo work, and **re-inject after compact**.
4. Builtins are one line; MCP/skills on demand.
5. Compaction is a cheap-first cascade, then rebuilds what must survive.
6. Subagents return summaries only.
7. Negatives are few and ranked. They do not fight the first line.

Steal jobs, not Claude terracotta / OMP XML / GrokNight voice.

---

## Phase 1 — Carina inventory (v0.8.33 files)

### Assembly

| File | Role |
|------|------|
| `go/daemon/agent.go` | `productIdentity` 455 B; `conversationFirst` 1258 B; `productCapabilityBrief` 1254 B; `toolsHelp` **4806 B / ~1202 tok**; concat `systemPrompt` **~8457 B / ~2115 tok** |
| `go/daemon/agents.go` | default interactive `converse`; build/plan prepend one sentence onto the same const |
| `go/daemon/promptcache.go` | `composeAgentPromptLayers` once/run: Constitution → Workspace → Catalog; TASK+TRANSCRIPT+closing in `VolatileSuffix` |
| `go/daemon/prompt_mode.go` | `shouldLoadProjectInstructions`: build/plan always; converse only `looksLikeRepoWork` |
| `go/daemon/memory.go` | `loadMemory` 8 kB cap; CARINA.md / AGENTS.md root→cwd, one winner/dir |
| `go/daemon/grok_reasoner.go` | `grokACPSystemPrompt` = Carina JSON ReAct; user = `grokACPPromptPrefix` + `seg.full()` |
| `go/daemon/tool_schema.go` | native `carinaToolSpecs()` when HTTP ToolCall and **not** Grok/Claude CLI |
| `go/daemon/transcript.go` | `compact(nil)` before Think (elide/collapse); model summary after the turn |
| `go/daemon/subagent.go` | parent sees `done.summary` only |
| `go/daemon/explore.go` | lean `exploreToolsHelp` |
| `go/contextengine` | identity / noop |

Hot path every turn: `tr.compact(nil)` → `seg.full()` string concat → reasoner.

### Forced answers

1. **How:** one `const systemPrompt` concat. Layers freeze per run. Anthropic may `cache_control` on Constitution/Workspace/Catalog. Grok/OpenAI/`claude CLI` = `promptCacheKindFor` **`"none"`**.
2. **Patterns:** 1 (monolith), 2 (Grok/OpenAI uncached; Catalog still keyed off `task.UserPrompt` for skills), 3 (full `toolsHelp`), 11 (`full()` rebuild).
3. **Reference:** Claude `string[]` + boundary; jcode `SplitSystemPrompt`; Grok compact **re-injects AGENTS.md** as its own item (`assemble.rs`).

### What 0.8.31 actually closed

| ID | Was | Now |
|----|-----|-----|
| S1 | default **build** “inspect the workspace” | default **converse** |
| S2 | greeting rule buried under tools | `conversationFirst` **above** `toolsHelp` |
| S3 | 8 k AGENTS.md on `hi` | F only build/plan or repo-work heuristic |
| S4 | TASK inside cache prefix | TASK+transcript in `VolatileSuffix` |
| S5 | Grok system = “plain data / do not call tools” | Grok system = JSON ReAct |
| S6 | “batch all independent search on first exploration” | batch only if the turn needs evidence |

Fixture G (greeting, AGENTS.md present → `done`, 0 tools) is a **prompt prefix** test plus a stub reasoner. It is not proof a live Grok turn obeys B. Anti-pattern 1 in `PROMPT_SPEC.md` still applies.

---

## Phase 2 — Broken patterns (0.8.33)

One hit = FAIL. “S1–S6 shipped” is not a pass.

| # | Pattern | Now | Evidence |
|---|---------|-----|----------|
| 1 | Monolithic system prompt | **FAIL** | still `const systemPrompt = identity + B + brief + toolsHelp + orchestration` |
| 2 | No cache-aware split | **PARTIAL** | TASK out of prefix; Anthropic sections exist; Grok/OpenAI `none`; skill catalog from user text |
| 3 | All tool schemas every turn | **FAIL** | `toolsHelp` ~1202 tok every JSON-ReAct turn; native path **replaces** toolsHelp with `nativeToolsContract` but `carinaToolSpecs()` is the full set |
| 4 | Raw tool results | **PARTIAL** | snip 2 k + elide KeepRecent 3; no structured extract |
| 5 | Only fire-at-cap compact | **PARTIAL** | cheap cascade + summary **off** Think stack (0.8.33); still no proactive/semantic; contextengine noop |
| 6 | Compact drops rules | **PARTIAL** | frozen layers survive; **no** Grok-style verbatim AGENTS re-inject / file rebuild |
| 7 | No token pressure | **PARTIAL** | `/context` 80/90; `estimateTokens = len/4+1` |
| 8 | Subagent transcript dump | **PASS** | summary-only |
| 9 | Adjective pile / no rank | **PARTIAL** | B is first; capability brief still always in; toolsHelp still Never×8 / Do not×6 / ONLY×7 |
| 10 | Role pollution | **PARTIAL** | Grok system is ReAct; user still receives the **entire** `seg.full()` constitution as the “request” |
| 11 | Hot-path full rebuild | **PARTIAL** | layers frozen; `full()` concatenates every turn |
| 12 | Memory no budget | **PARTIAL** | 8 k project docs + frozen snapshot; greeting no longer dumps F |
| 13 | Spec mixed with examples | **FAIL** | JSON examples live inside `toolsHelp` |
| 14 | Excess DO NOT | **FAIL** | protocol wall still fights “one line of converse” |

**0.8.33 prompt system: 1 PASS, 8 PARTIAL, 5 FAIL.** Better than 19’s 1/4/9. Still a product FAIL.

---

## Phase 3 — Scores

| Dimension | Best reference | Carina now | Score | Pattern | Pri |
|-----------|----------------|------------|-------|---------|-----|
| Prompt structure & cache | Claude boundary; jcode split | 3 frozen layers; Grok/OpenAI none; one const | **4** | 1, 2, 10 | **P0** |
| Instruction hierarchy | Grok/OMP documented deeper-wins | converse first; F gated; brief always-on | **6** | 9 | **P0** |
| Tool loading | Claude lazy schema | 1202 tok toolsHelp every JSON turn | **4** | 3, 13 | **P0** |
| Compaction quality | Claude 4-tier + rebuild; Grok AGENTS re-inject | snip/elide/summary; no rebuild | **6** | 5, 6 | P1 |
| Persistent-instruction lifecycle | Grok verbatim re-inject | demand-load + freeze | **6** | 6 | P1 |
| Memory inject | OMP 5 k / Codex progressive | frozen snapshot + optional HMS | **5** | 12 | P1 |
| Subagent isolation | Claude workers | summary-only | **8** | — | keep |
| Token observability | Claude window math | len/4 + `/context` | **5** | 7 | P1 |

**Overall: FAIL ~5.5/10.**

Gap in one sentence: Carina now **says** converse-first, but still **pays** a 2 k-token tool encyclopedia and an always-on capability brief on every Think, and only Anthropic can cache any of it.

---

## Phase 4 — Remaining knives

Do not re-open S1–S6. Do not enable MiniLM / snapcompact / wholesale Grok pager.

### P0 (prompt product force)

| ID | Problem | Steal | Change | Accept |
|----|---------|-------|--------|--------|
| P0-S7 | Constitution is one Go concat | Claude `string[]`; jcode split | Named sections A–D as a list; Anthropic one `cache_control` per section | `systemPrompt` is not a single string; tests assert section order |
| P0-S8 | `toolsHelp` 4806 B / ~1202 tok with examples | Claude one-line tools; MCP find | Builtins: one line each. Protocol C ≤ 5 standing negatives. Examples in tests | Constitution tokens without Workspace/F < **800** |
| P0-S9 | Capability brief always in A | Claude CLAUDE.md after boundary | Inject brief only when the operator asks who/what/limits (`looksLikeProductQuestion`) | `hi` prefix contains no `PRODUCT CAPABILITY BRIEF` |
| P0-S10 | Skill catalog keyed off `UserPrompt` | Claude static builtins | Catalog for a run is MCP index + skill **names**; bodies stay `skill://` | `CacheSections` independent of greeting wording |
| P0-S11 | Native tools still paste JSON catalog on fallback | — | Native path must not also include `toolsHelp` examples | `withToolContract` leaves a ≤200 B protocol, not 4.8 kB |

### P1

- Post-compact rebuild: re-read F + ≤5 cited files (Claude rebuild; Grok AGENTS item).
- Provider usage tokens instead of `len/4` when present.
- Structured search/list extract (paths + 1-line why).
- Document Grok/OpenAI as uncached adapters until a real prefix-cache exists.

### P2

- Proactive EWMA compact (jcode). Semantic compact. Per-turn memory retrieve budget.

### Anti-patterns

1. Do not treat stub `conversation_first_test.go` as live-model obedience.
2. Do not add more DO NOTs to shrink Explore×N.
3. Do not dump AGENTS.md on `hi` “for context”.
4. Do not put TASK back in the cache prefix.
5. Do not label Carina ReAct as Grok “plain data”.
6. Do not enable MiniLM / snapcompact as the default.
7. Do not copy child transcripts into the parent.
8. Do not compact the audit chain.
9. Do not copy Claude/OMP/Grok prompt prose into Carina identity.
