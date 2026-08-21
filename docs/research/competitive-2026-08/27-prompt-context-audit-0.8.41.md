# Carina Prompt / Context Engineering Audit (v0.8.41)

> Date: 2026-08-21  
> Carina: **v0.8.41** source (`97f359b`) plus uncommitted intent/session-dialogue knife.  
> Role: Context Engineering Architect. “Agent can reply” is not a pass.  
> Prior: `19` (v0.8.30 FAIL), `21` (v0.8.33 FAIL ~5.5). Those audits closed S1–S11 as *structure*. This round re-opens the **product** bar.  
> Law: `docs/PROMPT_SPEC.md`, `docs/CONTEXT_ENGINEERING.md` (rewritten with this evidence).

**Verdict: FAIL ~5.8/10 as a prompt system.** Named A–D, one-line builtins, F mode-gated, TASK out of prefix, subagent summary-only, constitution without F ~788 tok — those landed. The default interactive path still stuffs constitution into the **user** role, Grok/OpenAI/Mox cache kind is `none`, and a live 执行-mode session answered「这个 repo 是什么 / 分析 repo」from product identity without tools.

---

## Phase 0 — Ruler (source, not marketing)

| Job | Claude Code (notes v2.1.88) | OMP | Grok Build | Codex | jcode | DeepSeek |
|-----|-----------------------------|-----|------------|-------|-------|----------|
| Shape | `string[]` + `__DYNAMIC_BOUNDARY__` | Handlebars modules → XML-tagged sections | MiniJinja + agent MD | `prompt.md` + model fragments | `SplitSystemPrompt { static_part, dynamic_part }` | **ABSENT** (no local clone) |
| Cache | static `system` + `cache_control` `scope: global`; CLAUDE.md **after** boundary (often user) | soft; rules survive compact | stable system; volatile `<system-reminder>` | fragments re-injected post-compact | explicit static vs dynamic | — |
| Rules | CLAUDE.md cwd↑ + `~/.claude`; deeper wins | native/claude/agents priority | AGENTS **verbatim re-inject** after compact (`assemble.rs` `new_project_instructions`) | AGENTS root→cwd + byte cap | project + `~/AGENTS` into **static** | — |
| Tools | lazy Zod; builtins before MCP | inventory in prompt; `skill://` | schemars registry | ToolSpec JSON | sorted schemas + forced `intent` | — |
| Compact | snip → image → collapse XOR autocompact (~167k/200k) then **rebuild files** | threshold/overflow/idle; snapcompact **optional, not default for Carina** | ~85% full-replace; assemble `[SP, UP', AGENTS?, UQ, recent, summary]` | token-limit + remote handoff | 80% + **proactive EWMA** + **semantic** (P1) | — |
| Memory | CLAUDE.md + memdir caps | session-start ≤5k tok | first-turn search; freeze block; `/flush` `/dream` | 2500 tok summary + progressive files | async N→N+1 into **dynamic**; MiniLM **optional, not Carina default** | — |
| Subagent | workers: no parent chat; fork: shared prefix | isolated process | own prompt; compacted AGENTS optional | child context modules | swarm overlay in dynamic | — |

Evidence paths:

- Claude: `claude-code-notes/06-服务与基础/09-system-prompt工程.md`, `10-上下文压缩.md`
- Grok: `xai-grok-compaction/src/code_compaction/assemble.rs` L70–76; `item.rs` `new_project_instructions`
- jcode: `jcode-base/src/prompt.rs` `SplitSystemPrompt`
- OMP: DNA `01-dna-omp.md` — steal compaction cascade and skill `skill://`, not XML voice or default snapcompact
- Codex: DNA `01-dna-codex.md` — steal AGENTS layering + continuation, not pets/fuzzy monopoly

**Ruler (all must be true to pass):**

1. Constitution is short named sections, in the **system** (or provider-system) role, cacheable without TASK/repo docs/tool examples.
2. Mode is the first operative sentence.
3. Identity and F are constraints. They are not a description of this workspace.
4. Project rules load when the turn is repo work, and **re-inject after compact**.
5. Builtins are one line; MCP/skills on demand.
6. Compaction is cheap-first, then rebuilds what must survive.
7. Subagents return summaries only.
8. Negatives are few and ranked. They do not fight the first line.
9. Default interactive models (not only Anthropic) have an honest cache story — `none` is allowed if labeled, not if the UI implies prefix cache.

Steal jobs, not Claude terracotta / OMP XML / GrokNight / jcode MiniLM-on.

---

## Phase 1 — Carina inventory (live files)

| File | Role |
|------|------|
| `go/daemon/agent.go` | `productIdentity` 418 B; `intentFirst` 840 B; `harnessProtocol` 951 B; `toolsCatalog` 784 B; `coreConstitution` ~788 tok without F |
| `go/daemon/agents.go` | default `converse`; build/plan one-line openers |
| `go/daemon/promptcache.go` | `composeAgentPromptLayers` once/run; `CacheSections` A–D then workspace/catalog; `full()` concat |
| `go/daemon/prompt_mode.go` | F only `build`/`plan`; converse never host-dumps AGENTS.md |
| `go/daemon/anthropic.go` | **No `system` field.** Constitution is **user** content blocks with ≤4 `cache_control: ephemeral` |
| `go/daemon/context_summary.go` | `promptCacheKindFor`: Anthropic protocol → `"anthropic"`; Grok/OpenAI/Claude CLI/`ccswitch` OpenAI → `"none"` |
| `go/daemon/grok_reasoner.go` | `systemPromptOverride` = 1-line ReAct; user = `grokACPPromptPrefix` + `seg.full()` |
| `go/daemon/tool_schema.go` | native envelope 167 B; `carinaToolSpecs()` full HTTP tools array when `nativeToolsEligible` |
| `go/daemon/transcript.go` | snip 2k; KeepRecent 3; elide; model summary **after** Think |
| `go/daemon/compact_rebuild.go` | ≤5 cited files into volatile `Transcript.Rebuild` |
| `go/daemon/compaction_budget.go` | catalog window − reserve; trigger 80%; alias lookup for proxy models |
| `go/daemon/memory.go` | CARINA.md/AGENTS.md root→cwd, 8 kB, one winner/dir |
| `go/daemon/session_dialogue.go` | prior operator/assistant pairs into transcript (not constitution) |
| `go/daemon/subagent.go` | parent gets `act.Summary` only |
| `go/contextengine` | identity/noop; `Transcript.compact` is the compressor |
| `go/daemon/agent.go` `estimateTokens` | `len/4+1` |

Hot path every Think: `tr.compact(nil)` → `seg.full()` string concat → reasoner. Layers are frozen per run. The **string** is rebuilt every turn.

### Forced answers

1. **How:** named A–D list in Go; Anthropic caches **user** blocks; Grok system is one ReAct line and the constitution rides in the user request; OpenAI/Mox is one blob, cache `none`.
2. **Broken patterns:** 2 (default path uncached), 10 (constitution in user), 11 (`full()` rebuild), 9 (identity answered「这个 repo」). 1 is PARTIAL (named sections, still concatenated). 3 is PARTIAL (one-line D; native HTTP still ships full specs — acceptable for FC).
3. **Reference:** Claude `system[]` + boundary; jcode `SplitSystemPrompt`; Grok compact **re-injects AGENTS.md** as its own item.

---

## Phase 2 — Broken patterns (0.8.41)

One FAIL = product FAIL. “S1–S11 landed” is not a pass.

| # | Pattern | Now | Evidence |
|---|---------|-----|----------|
| 1 | Monolithic system prompt | **PARTIAL** | named A–D exist; `full()` still one string; Grok/OpenAI consume concat |
| 2 | No cache-aware split | **FAIL** on default Mox/OpenAI/Grok (`none`). **PARTIAL** on Anthropic (user-block ephemeral, no `scope: global`, constitution not in `system`) |
| 3 | All tool schemas every turn | **PARTIAL** | JSON ReAct D is one-liners (~196 tok). Native `carinaToolSpecs()` is the full HTTP set. MCP schemas stay behind `mcp_find` |
| 4 | Raw tool results | **PARTIAL** | snip 2k + list/search extract; no image-collapse tier; no enqueue-time token truncate like Claude L1 |
| 5 | Only fire-at-cap compact | **PARTIAL** | cheap cascade + summary off Think; no proactive/EWMA |
| 6 | Compact drops rules | **PARTIAL** | F frozen in Workspace for the run; **no** Grok-style AGENTS item after compact; cited-file rebuild exists |
| 7 | No token pressure | **PARTIAL** | `/context` 80/90; provider usage when present; else `len/4` |
| 8 | Subagent transcript dump | **PASS** | `subagent.go` returns `act.Summary` |
| 9 | Adjective pile / no rank | **FAIL** (live) | Mode/Intent ranked; a 执行 session still answered repo questions from identity/F without tools |
| 10 | Role pollution | **FAIL** | Anthropic `messages=[user: constitution+TASK]`; Grok user carries `seg.full()` |
| 11 | Hot-path full rebuild | **PARTIAL** | layers frozen; `full()` concatenates every Think |
| 12 | Memory no budget | **PARTIAL** | 8k F + frozen snapshot; HMS optional; no per-turn retrieve budget |
| 13 | Spec mixed with examples | **PASS** | JSON examples in tests, not D |
| 14 | Excess DO NOT | **PARTIAL** | C has 5 bullets; session-dialogue “names no new object” over-taught deixis (uncommitted fix) |

**0.8.41 prompt system: 2 PASS, 8 PARTIAL, 4 FAIL.** Better structure than 21. Still a product FAIL.

---

## Phase 3 — Scores

| Dimension | Best reference | Carina now | Score | Pattern | Pri |
|-----------|----------------|------------|-------|---------|-----|
| Prompt structure & cache | Claude `system[]` + boundary | named A–D; stuffed into **user**; Grok/OpenAI `none` | **5** | 2, 10 | **P0-P12** |
| Instruction hierarchy | Grok/OMP documented deeper-wins | converse first; F gated; identity answered the repo | **5** | 9 | **P0-P13** |
| Tool loading | Claude lazy MCP | one-line D; native full specs; mcp_find | **6** | 3 | P1 |
| Compaction quality | Claude 4-tier + rebuild; Grok AGENTS re-inject | snip/elide/summary/rebuild; no AGENTS item; no proactive | **6** | 5, 6 | P1 |
| Persistent-instruction lifecycle | Grok verbatim re-inject | demand-load + freeze | **6** | 6 | P1 |
| Memory inject | OMP 5k / Codex progressive | frozen snapshot + 8k F | **5** | 12 | P1 |
| Subagent isolation | Claude workers | summary-only | **8** | — | keep |
| Token observability | Claude window math | `/context` + `len/4` | **5** | 7 | P1 |

**Overall: FAIL ~5.8/10.**

Gap in one sentence: Carina now **has** a constitution list, but still **pays** it as a user blob on every Think, and the default model cannot cache it — then treats that blob as enough evidence to skip the workspace.

---

## Phase 4 — Knives

Do not re-open S1–S11 as undone. Do not enable MiniLM / snapcompact / wholesale Grok pager / 奏折 / git-first-class / browser.

### P0 (prompt product force)

| ID | Problem | Steal | Change | Accept |
|----|---------|-------|--------|--------|
| **P0-P12** | Constitution lives in the user role. Anthropic cache_control is on user blocks. Grok/OpenAI/Mox = `none` | Claude `system[]` + `SYSTEM_PROMPT_DYNAMIC_BOUNDARY`; jcode `SplitSystemPrompt` | Anthropic: A–D in `system` with `cache_control`. User = TASK+transcript+closing. Grok: keep short `systemPromptOverride`; do not stuff A–D as “the request” if the API can take system. Document `none` as honest | Anthropic request JSON has `system` array; `hi` user block contains TASK not `You are Carina`. Test: `cache_control` not on TASK. Fixture: `/context` `cache=anthropic` shows constitution bytes in system, not user |
| **P0-P13** | Identity/F answered「这个 repo / 分析 repo」with no tools | meta intent, not a phrase classifier | Intent: identity and project instructions are not this repository. Session dialogue must not teach “这个” = this conversation | Stub Fixture G still `done` on `hi`. Live: 执行 + 「分析 repo」first action is list/map/read, not `done` with a product blurb. No host utterance classifier |
| **P0-P14** | Cache kind is a label, not a measurement | Claude usage `cache_read_input_tokens` | Surface Anthropic `CacheReadTokens` on `/context` and in doctor. Fail the claim if read=0 across a 3-turn same-run Think | Three Thinks same run, same workspace: `cache_read > 0` on turn 2–3 for Anthropic. Grok/OpenAI stay `none` without lying |

### P1

| ID | Item |
|----|------|
| P1-C5 | Grok-style AGENTS.md item after compact (build/plan only; never greetings) |
| P1-C3 | Proactive compact (jcode EWMA) before hard 80% |
| P1-C6 | OpenAI Responses prefix cache **if** the route documents it; otherwise keep `none` |
| P1-C7 | Native tools: keep schemas in HTTP array (correct); do not also paste D on native path (already S11) |
| P2-C2 | Memory retrieve budget per turn |

### Anti-patterns (absolute)

1. Do not treat stub `conversation_first_test.go` as proof a live model inspects the repo.
2. Do not call `cache_control` on user-stuffed constitution “prompt caching done”.
3. Do not dump AGENTS.md on `hi` “because compact dropped it”.
4. Do not enable MiniLM / snapcompact as defaults.
5. Do not copy Claude terracotta / OMP XML / GrokNight into Carina identity.
6. Do not add more DO NOTs to paper over a wrong role (system vs user).
7. Do not return child transcripts to the parent.
8. Do not classify operator phrases in the host to splice F back into converse.
