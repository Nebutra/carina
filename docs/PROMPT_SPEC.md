# Carina Prompt Specification

Status: hardened implementation specification after the `v0.8.42` prompt/
context audit. This document remains the normative target. Cache SLO and schema
share are now directly observable; provider-backed cache runs and end-to-end
long-session quality curves remain release evidence, not implied by unit tests.

The acceptance bar is prompt/context quality, not merely a successful answer.
A prompt is accepted only when its sections, roles, cache boundary, loading
policy, and lifecycle are observable and testable.

## 1. Reference standard

The standard was extracted from source and design notes, in this order of
weight:

| System | Evidence read | Engineering rule adopted |
|---|---|---|
| Claude Code | `/Users/tseka_luk/workspace/assets/references/claude-code-notes/06-服务与基础/09-system-prompt工程.md`, `10-上下文压缩.md`, `04-Agent协调/06-Fork与提示词缓存优化.md` | section arrays, `SYSTEM_PROMPT_DYNAMIC_BOUNDARY`, layered instruction files, cascade compaction, summary-only forks |
| oh-my-pi (OMP) | `/Users/tseka_luk/workspace/assets/references/competitors/oh-my-pi/packages/coding-agent/src/system-prompt.ts`, `capability/system-prompt.ts`, `docs/compaction.md`, `docs/context-files.md`, `docs/memory.md` | modular prompt parts, sticky rules, deferred context files, bounded memory |
| Grok Build | `/Users/tseka_luk/workspace/assets/references/competitors/grok-build/crates/codegen/xai-grok-agent/src/prompt/{template.rs,context.rs,skills.rs}`, `xai-grok-shell/src/session/{compaction.rs,acp_session_impl/memory_dream.rs}` | discoverable skills, layered project rules, compaction recovery and memory dream |
| jcode | `/Users/tseka_luk/workspace/assets/references/competitors/jcode/docs/MEMORY_ARCHITECTURE.md`, `MEMORY_BUDGET.md`, `crates/jcode-base/src/{prompt.rs,compaction.rs}` | proactive/semantic compaction, explicit memory budget and retrieval |
| Codex | `/Users/tseka_luk/workspace/assets/references/competitors/codex/codex-rs/core/{gpt-5.2-codex_prompt.md,src/agents_md.rs,src/agents_md_manager.rs,src/compact.rs}` | AGENTS discovery/override, model-specific prompt modules, compact rebuild |
| DeepSeek Harness | no standalone harness source or design document was found under the local reference tree | no implementation claim; use only as an open research gap |

## 2. Canonical sections and boundary

The live prompt is a list of sections. `StablePrefix` is an equivalent wire
representation for adapters that cannot accept arrays; it is not permission
to author one monolithic prompt.

| ID | Section | Lifetime/cache | Wire role | Required content | Prohibited content |
|---|---|---|---|---|---|
| A | Identity | static, cacheable | system | Carina/Nebutra identity and product boundary | repository facts, task, tool schemas |
| B | Mode | static per run, cacheable | system | first operative instruction for `converse`, `build`, `plan`, or subagent | instructions for another mode |
| C | Protocol | static per protocol, cacheable | system | short JSON ReAct or native-tool contract | examples, full catalog, policy metadata |
| D | Tools | stable builtin index, cacheable where supported | system | one-line builtin descriptions and discovery routes | MCP schemas, full skill bodies |
| E | Workspace | run-dynamic, after cache boundary | system dynamic block or provider equivalent | cwd, locale, sandbox and runtime scope | TASK, transcript |
| F | Project rules | mode-gated, after boundary | system dynamic block | global to nested `CARINA.md`/`AGENTS.md` content with provenance | greeting-time host injection |
| G | Memory snapshot | frozen at task start, after boundary | system dynamic block | bounded governed facts | raw session transcript or untrusted instructions |
| H | TASK | every turn | user | exact operator request | A-D constitution |
| I | TRANSCRIPT | every turn | user | bounded model projection | append-only audit log |
| J | Closing | every turn | user | next action / next tool / `done` | new standing policy |

`SYSTEM_PROMPT_DYNAMIC_BOUNDARY` is the conceptual boundary between D and E.
Carina represents it with provider-facing `SystemSections` (A-D) and
`DynamicSections` (E-G). `promptLayers.StablePrefix` is the memoized A-G wire
form used by flat adapters and Direct OpenAI's cache key. Cache receipts hash
the actual adapter boundary: A-D for Anthropic and the frozen A-G prefix for
Direct OpenAI. A cache hit is valid only when provider usage reports it; a user
message containing A-D is not a system cache boundary.

## 3. Precedence and overrides

The runtime, not model prose, enforces capability policy. For prompt content,
the following order is normative (higher wins):

1. Kernel capability and fail-closed policy.
2. Active mode (B).
3. Operator message (H), including explicit steering.
4. Project rules (F), when the mode loads them.
5. User-level `CARINA.md` rules.
6. Governed memory snapshot (G).
7. Tool descriptions (D).
8. Identity (A).

Instruction files load from user scope, project root, then nested directories;
the more specific directory wins conflicts. Every injected rule must carry its
source path and a deterministic precedence order (persisted with the
checkpoint manifest). `CARINA.override.md` precedes
`CARINA.md`, and the `AGENTS.*` names are compatibility fallbacks. `converse`
does not host-load project rules; `build` and `plan` do. Compact/restart must
recompose the current rule set once in dynamic system section F before the next
model call. `Transcript.Rebuild` contains cited file evidence only and must not
carry a second or stale copy of section F.

## 4. Tool description and loading contract

- Builtins have one short, outcome-oriented description. Examples belong in
  tests or a deferred reference, not in C or D.
- Native HTTP schemas are sent in the provider `tools` field and are projected
  through the active session allow/deny lists and plan gate. The compact text
  index uses the identical projection. Do not duplicate native schemas in C.
- MCP exposes an index (name plus 60-120 character description). `mcp_find`
  resolves the full schema immediately before a call.
- Skills expose names and trigger metadata first. The body is read only after
  activation; requested skill warnings are volatile and sit before H.
- Explore subagents receive only the lean read-only tool set and no project
  rules, shell, MCP, write, or spawn contract. Other child agents use the same
  named A-D sections as the parent, with a session-filtered tool index.
- A tool result is a bounded observation with typed importance, status,
  provenance, and a shape summary plus artifact pointer when truncated.
  Ordinary outputs are capped at the active
  tool-output limit; pinned outputs over 64 KiB become a recoverable pinned
  artifact pointer. Raw bytes remain in the artifact/event ledger, never in I
  or a parent subagent context.

## 5. Adapter contract

Every provider adapter must declare its prompt role and cache behavior:

| Adapter | Required mapping | Cache declaration |
|---|---|---|
| Anthropic Messages | A-D in `system` blocks with cache breakpoints; E-G uncached system blocks; H-J in user | report cache read/write usage |
| Direct OpenAI HTTP | frozen A-G wire prefix plus H-J in user input; schemas in `tools` | stable `prompt_cache_key`; report cached input tokens as `openai_key` receipts |
| Other native HTTP route | provider-compatible instruction/input mapping; schemas in `tools` | `none` unless provider usage proves cache support |
| CLI/ACP vendor route | one coherent envelope only; do not label a Carina user blob as a system prompt | `none` unless the adapter proves otherwise |

The fallback path must preserve role semantics. If a provider cannot carry
sections, it must be explicitly marked uncached and the loss recorded in the
routing event; it must not silently stuff the constitution into TASK.

## 6. Assembly invariants

- `composeAgentPromptLayers` is called once per run. Its layer object and
  serialized stable prefix are frozen for that run. Cross-run memoization is
  allowed only with an explicit key covering workspace, mode, locale, provider
  protocol, tool registry version, instruction revision, and memory revision.
- `buildPromptSegmentsFromLayers` may be called per turn for the volatile
  suffix, but it must reuse the memoized serialized stable sections and stable
  hash. A native tool-contract variant must invalidate only that prefix cache.
- TASK and transcript bytes must never occur before the dynamic boundary.
- Requery/fallback may append a local recovery hint to the volatile suffix; it
  may not mutate A-D.
- Summary, compaction receipt (including v4 elision-only receipts), cache usage/SLO, actual cache-boundary hash,
  native schema count/share, and role mapping are emitted as structured
  telemetry so `/context` can explain what the model saw.

## 7. Acceptance tests

The following are release gates, not aspirational examples:

1. Stable A-D hash is identical across turns 2+ of one run; at least 95% of
   cache-eligible turns after the first report a provider-proven hit over at
   least 20 measured post-warm-up requests.
2. `hi` in `converse` produces `done` without list/read/search/code calls; a
   repository question produces an evidence tool call before a product answer.
3. Native tool schemas are filtered to the active exposure set and occupy less
   than 15% of the input budget on a 32k-window fixture.
4. No observation reaches the model above its configured cap except a pinned
   fail-closed pointer; every truncation has an artifact reference or hash.
5. Compaction preserves the task, explicit user steering, changed paths,
   failing checks, next action, and the active project-rule revision.
6. A compact/restart/resume fixture yields exactly one current effective F rule
   set, no stale rule text, and the same memory revision before the next action.
7. Parent subagent context contains a typed summary only; no child transcript
   or raw tool body is copied into the parent projection.
8. `/context` reports window, reserve, trigger, pressure, estimation method,
   cache read/write, warm-up-adjusted cache SLO, compaction receipts, native
   schema share, and layer byte/token shares.

## 8. Absolute prohibitions

Do not use a giant hand-authored system string as the canonical representation.
Do not call a user-block cache breakpoint a system prompt cache boundary. Do
not inject every native/MCP schema or every skill body on every turn. Do not
answer repository questions from product identity. Do not use more negative
rules to compensate for an incorrect role or mode. Do not return subagent
transcripts to the parent. Do not claim SOTA or release readiness while any
release gate or the audit FAIL table is open.
