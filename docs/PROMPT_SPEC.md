# Carina Prompt Spec (DRAFT LAW)

> Status: **P0 law**. Evidence: audit 19 (v0.8.30) and re-audit 21 (v0.8.33).
> `docs/research/competitive-2026-08/19-prompt-context-audit.md`,
> `docs/research/competitive-2026-08/21-prompt-context-re-audit.md`.
> Code: `go/daemon/agent.go`, `promptcache.go`, `prompt_mode.go`,
> `agents.go`, `tool_schema.go`, `grok_reasoner.go`.
>
> S1–S11 prompt-law P0 is closed. S8: constitution without Workspace/F < 800
> tok (`len/4`). S7: named A–D. S10: REQUESTED skills out of Catalog. S11:
> native envelope ≤ 200 B. Context-engineering C1–C2 closed (Fixture G/R
> first 3 turns; compact is not a context engine).

Steal jobs, not strings. Do not copy Claude’s terracotta voice, OMP π, or
GrokNight into Carina identity.

---

## 1. Segments (ordered)

Constitution is a **list of named sections**, not one Go string. Anthropic
gets one cacheable block per stable section. Grok/OpenAI still consume
concatenated text but **must not** put constitution in a user blob labeled
“plain data”.

| # | Section | Cache | Contents | Must not contain |
|---|---------|-------|----------|------------------|
| A | Identity | static | 4–8 lines: Carina / Nebutra; not Claude/Codex/GPT | tools, repo rules, TASK |
| B | Mode | static per run | **First operative sentence.** `converse` vs `build` vs `plan` | the other mode’s verbs |
| C | Protocol | static | JSON ReAct or native-tools contract, **short** | full tool catalog examples |
| D | Tools | static builtins | builtins only; MCP/skills as **index** | MCP JSON schemas, skill bodies |
| E | Workspace | dynamic | cwd, os, locale, sandbox | AGENTS.md, TASK |
| F | Project rules | dynamic, **mode** | CARINA.md / AGENTS.md on build/plan | converse host-load; greetings |
| G | Memory snapshot | dynamic, frozen/run | retrieved facts with budget | full memory dump |
| H | TASK | volatile | operator message | constitution |
| I | TRANSCRIPT | volatile | compacted model view | audit log |
| J | Closing | volatile | one line: next JSON / next tool / `done` | new policy |

**Boundary:** A–D are cacheable constitution. E–G may change between runs
but not every turn. H–J change every turn. Do not put H inside A–D.

---

## 2. Mode is the first line

Default **interactive** agent is `converse`, not `build`.

```
converse: Answer the operator's actual intent. Tools only when the
          message needs workspace evidence or a side effect.
build:    Change the workspace. Inspect only what the task needs.
plan:     Read-only. Answer or produce a plan. No edits, no shell.
```

`go/daemon/agents.go` used to prepend build with “Inspect the workspace”.
That sentence overrode the greeting rule later in `toolsHelp`. The default
interactive agent is now `converse`; build’s opener is inspect-only-what-the-task-needs.

Fixture G (acceptance): in this repo, with `AGENTS.md` present, prompt `hi`
→ first model action is `done`, zero list/read/search/code.*.

---

## 3. Override rank (high wins)

1. Capability kernel / fail-closed policy (not in the prompt)
2. Mode (B)
3. Operator message (H)
4. Project rules (F) when loaded
5. User CARINA.md
6. Memory snapshot (G)
7. Tool catalog (D)
8. Identity (A)

If 2 and 4 conflict, mode wins. converse does not dump F; the model may
read project instructions with a tool if the ask needs them. Do not
classify operator phrases in the host to splice F back in.

---

## 4. Tool descriptions

- Builtins: one line each. Examples belong in tests, not constitution.
- Native schemas: same names as JSON actions; do not also paste `toolsHelp`.
- MCP: names + 60–120 char blurbs; full schema only after `mcp_find`.
- Skills: stable catalog of names + slash commands. Bodies via `skill://`
  read. REQUESTED SKILLS / SKILL WARNING are volatile (before TASK), not Catalog.
- Explore subagent: keep lean `exploreToolsHelp`. Do not give it F.

---

## 5. Writing rules

- Ranked bullets. No “always / never / be careful” stacks that fight B.
- At most **five** standing negative rules in C.
- PRODUCT CAPABILITY BRIEF stays out of the live constitution. Intent is meta, not a host-side phrase classifier.
- `done.summary` is the only user-visible answer (keep).
- Parallel batch of list/read/search is allowed **after** the model has
  decided the turn needs workspace evidence — not as the default first move.

---

## 6. Grok / Claude CLI adapters

Isolation may disable **vendor** tools. It must not:

- Label Carina’s constitution as “plain data”
- Ask for JSON tool calls in the same blob that says “do not call tools”
- Skip prefix cache without documenting that the adapter is uncached

One envelope: either (a) Carina ReAct as the **system** text the model
is allowed to follow, or (b) vendor agent with vendor tools. Not both.

---

## 7. P0 knives

**Landed in 0.8.31:** S1 converse default; S2 conversation-first above tools;
S3 F demand-gated; S4 TASK in VolatileSuffix; S5 Grok JSON ReAct system;
S6 search batch only after evidence is needed.

**Landed in 0.8.34:** S9 capability brief out of live constitution. Intent is
meta (situated answer, not a matrix). `prompt_mode` is a mode switch, not a
phrase classifier. converse does not dump AGENTS.md.

**Landed (S8):** builtins are one line each; protocol C is five standing
bullets; JSON examples stay in tests. Converse constitution without
Workspace/F is under 800 tok (`len/4`). Native HTTP swaps protocol C for a
≤ 200 B envelope and drops tools D — do not paste the catalog back.

**Landed (S7):** constitution is a named list (Mode, Identity, Protocol,
Tools), not `const systemPrompt`. Anthropic attaches `cache_control` to A–D
(API cap 4). Grok/OpenAI still consume `full()` concat with cache kind `none`.
`withToolContract` replaces protocol C and drops tools D.

**Landed (S10):** skill Catalog is MCP index + skill names + slash commands
only. REQUESTED SKILLS and SKILL WARNING sit in VolatileSuffix before TASK.
`CacheSections` is independent of greeting wording (`hi` vs `Use $pdf`).
Skill bodies stay behind `skill://` read. Implicit triggers stay opt-in
(`CARINA_IMPLICIT_SKILL_PROMPTS`). `/review` stays a slash command
(ISSUE-030).

**Landed (S11):** `nativeToolsContract` is envelope-only (≤ 200 B). Schemas
stay in the HTTP tools array. `withToolContract` replaces protocol C, drops
tools D, and strips `toolsHelp` / `toolsCatalog` from legacy constitution
blobs. JSON ReAct catalog returns only on native requery fallback.

**Landed (T-S1 web.search):** public web query after host approval; HTTPS /
SSRF / untrusted / fail-closed same as `web.fetch`; plan+explore blocked;
never `run`/`curl`. Dispatch + native schema.

**Landed (T-S2 todo / update_plan):** real session checklist tools (dispatch +
schema). Plan mode allows them (`isReadOnlyTool`); explore does not. Empty
call echoes; empty slice clears. Not a phrase classifier.

**Landed (T-S3):** search groups by file (count + first line); list rolls up
dirs + sample files. Raw 50-line grep / 200-file trees are not the observation.

Prompt-law P0 (S1–S11) is closed. Tool-surface P0 (T-S1 web.search, T-S2
todo/update_plan, T-S3 search/list extract, S10) is closed. Context-engineering
P0 (C1 Fixture G/R constitution < 800 tok on the first 3 live turns; C2
`go/contextengine` identity, compact is not a context engine) is closed.
Post-compact cited-file rebuild is volatile (P1-C1). Provider usage drives
pressure when present (P1-C2). Remaining items are labeled P1/P2 or deferred
(`full()` rebuild, EWMA compact, 奏折, git-first-class, browser) — no unmarked
P0 knife rows.

Files: `go/daemon/agent.go`, `agents.go`, `promptcache.go`, `memory.go`,
`grok_reasoner.go`, `tool_schema.go`, `conversation_first_test.go`.

---

## 8. Anti-patterns

1. Do not treat `conversation_first_test.go` (stub reasoner) as proof models obey greetings.
2. Do not prepend build/inspect on the default interactive agent.
3. Do not inject AGENTS.md to “give context” for `hi`.
4. Do not put TASK inside the cacheable constitution.
5. Do not stuff Carina’s system prompt as a Grok user message labeled non-instruction.
6. Do not add more DO NOTs to paper over a wrong first line.
7. Do not load every MCP schema or skill body every turn.
8. Do not copy Claude/OMP/Grok prompt prose into Carina identity.
