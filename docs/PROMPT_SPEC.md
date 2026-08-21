# Carina Prompt Spec (DRAFT LAW)

> Status: **P0 law.** Structure S1–S11 landed. **P12/P13/P14 are in source**
> (Anthropic `system` A–D, identity ≠ workspace, `/context` constitution_role).
> Grok/OpenAI still user-stuff constitution (`cache=none`). Evidence: audit 27
> (`docs/research/competitive-2026-08/27-prompt-context-audit-0.8.41.md`).
> Code: `go/daemon/agent.go`, `promptcache.go`, `prompt_mode.go`,
> `agents.go`, `tool_schema.go`, `grok_reasoner.go`, `anthropic.go`.
>
> “Agent can reply” is not a pass. Named sections are not a pass while
> constitution rides in the user role.

Steal jobs, not strings. Do not copy Claude’s terracotta voice, OMP π, or
GrokNight into Carina identity.

---

## 1. Segments (ordered)

Constitution is a **list of named sections**, not one Go string. Anthropic
must receive A–D as **`system`** blocks (P0-P12). Grok/OpenAI still consume
concatenated text today; they **must not** put constitution in a user blob
labeled “the request” once P12 lands.

| # | Section | Cache | Role (target) | Contents | Must not contain |
|---|---------|-------|---------------|----------|------------------|
| A | Identity | static | system | 4–8 lines: Carina / Nebutra; not Claude/Codex/GPT | tools, repo rules, TASK, a description of *this* workspace |
| B | Mode | static per run | system | **First operative sentence.** `converse` vs `build` vs `plan` | the other mode’s verbs |
| C | Protocol | static | system | JSON ReAct or native-tools contract, **short** | full tool catalog examples |
| D | Tools | static builtins | system | builtins only; MCP/skills as **index** | MCP JSON schemas, skill bodies |
| E | Workspace | dynamic | after boundary | cwd, os, locale, sandbox | AGENTS.md, TASK |
| F | Project rules | dynamic, **mode** | after boundary | CARINA.md / AGENTS.md on build/plan | converse host-load; greetings |
| G | Memory snapshot | dynamic, frozen/run | after boundary | retrieved facts with budget | full memory dump |
| H | TASK | volatile | user | operator message | constitution |
| I | TRANSCRIPT | volatile | user | compacted model view | audit log |
| J | Closing | volatile | user | one line: next JSON / next tool / `done` | new policy |

**Boundary:** A–D are cacheable constitution in **system**. E–G may change
between runs but not every turn. H–J change every turn. Do not put H
inside A–D. Do not put A–D inside H.

---

## 2. Mode is the first line

Default **interactive** agent is `converse`, not `build`.

```
converse: Answer the operator's actual intent. Tools when the
          message needs workspace evidence or a side effect.
build:    Change the workspace. Inspect only what the task needs.
plan:     Read-only. Answer or produce a plan. No edits, no shell.
```

Intent (with Mode): identity and project instructions are **not this
repository**. Greeting and who-you-are do not inspect. A repo/structure
ask does.

Fixture G (acceptance): in this repo, with `AGENTS.md` present, prompt `hi`
→ first model action is `done`, zero list/read/search/code.*.
A repo/structure ask (even after a greeting, even in build) is not Fixture G.

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

Identity ranks last on purpose. It must not outrank a workspace ask.

---

## 4. Tool descriptions

- Builtins: one line each. Examples belong in tests, not constitution.
- Native schemas: same names as JSON actions; do not also paste `toolsHelp`.
  Schemas travel in the HTTP `tools` array, not in C.
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
- Session dialogue teaches deixis to the last **topic**, not “this = the chat”.
  Referring to the last answer does not inspect the workspace.

---

## 6. Grok / Claude CLI adapters

Isolation may disable **vendor** tools. It must not:

- Label Carina’s constitution as “plain data”
- Ask for JSON tool calls in the same blob that says “do not call tools”
- Skip prefix cache without documenting that the adapter is uncached
- Stuff A–D into the user prompt and call that a system prompt

One envelope: either (a) Carina ReAct as the **system** text the model
is allowed to follow, or (b) vendor agent with vendor tools. Not both.

`promptCacheKindFor` is a label. Anthropic must show `cache_read` on turn
2+ of the same run (P0-P14). Grok/OpenAI may stay `none` if labeled.

---

## 7. P0 knives

**Landed (structure, 0.8.31–0.8.38):** S1 converse default; S2 conversation-first
above tools; S3 F demand-gated; S4 TASK in VolatileSuffix; S5 Grok JSON ReAct
system line; S6 search batch only after evidence is needed; S7 named A–D;
S8 one-line builtins, C ≤ 5, constitution without F < 800 tok; S9 capability
brief out of live constitution; S10 REQUESTED skills out of Catalog; S11
native envelope ≤ 200 B.

**Landed (tool surface):** T-S1 `web.search`; T-S2 `todo`/`update_plan`;
T-S3 search/list extract.

**Open (product, 0.8.41):**

| ID | Problem | Accept |
|----|---------|--------|
| **P0-P12** | Constitution used to ride in the Anthropic **user** turn | **Landed in source:** Anthropic `system` = A–D (cached) + workspace/catalog (uncached). User = TASK+transcript. Grok/OpenAI still `none` / user-stuffed until a later slice. |
| **P0-P13** | Identity/F answered「这个 repo / 分析 repo」without tools | Intent: identity and F are not this repository. Live 执行 + 「分析 repo」first action is list/map/read, not a product blurb. Fixture G on `hi` still `done` |
| **P0-P14** | Cache kind is unlabeled honesty for Grok/OpenAI, unmeasured for Anthropic | Same-run Think 2–3: Anthropic `cache_read > 0`. Grok/OpenAI remain `none` without implying prefix cache |

Do not mark this spec “P0 closed” while P12–P14 are open.

---

## 8. Anti-patterns

1. Do not treat `conversation_first_test.go` (stub reasoner) as proof models obey greetings or inspect repos.
2. Do not prepend build/inspect on the default interactive agent.
3. Do not inject AGENTS.md to “give context” for `hi`.
4. Do not put TASK inside the cacheable constitution.
5. Do not stuff Carina’s system prompt as a Grok/Anthropic **user** message.
6. Do not add more DO NOTs to paper over a wrong first line or a wrong role.
7. Do not load every MCP schema or skill body every turn.
8. Do not copy Claude/OMP/Grok prompt prose into Carina identity.
9. Do not call user-block `cache_control` “SYSTEM_PROMPT_DYNAMIC_BOUNDARY done”.
10. Do not classify operator phrases in the host to splice F into converse.
