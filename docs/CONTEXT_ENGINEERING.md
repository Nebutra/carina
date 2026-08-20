# Carina Context Engineering (DRAFT LAW)

> Companion to `docs/PROMPT_SPEC.md`. Code: `go/daemon/transcript.go`,
> `compaction_budget.go`, `memory.go`, `memory_store.go`, `subagent.go`,
> `explore.go`, `context_summary.go`, `go/contextengine` (noop).

---

## 1. Two ledgers

| Ledger | Owner | Bound |
|--------|-------|-------|
| Audit / events | hash chain | complete, never compacted |
| Model view | `Transcript` | window − reserve; snip → elide → summary |

Never feed the audit log to the model.

---

## 2. Compaction cascade (target)

Keep today’s cheap-first order. Add the missing outer tiers later.

| Tier | When | Keep | Drop |
|------|------|------|------|
| 0 Snip | enqueue | last 2k chars of a tool obs (pinned fail-closed) | raw tail |
| 1 Stale reads | newer read of same path | latest | older unpinned reads |
| 2 Elide | pressure ≤ ~1.10 | last `KeepRecent` (3) turns; user turns under 4k | old tool bodies → pointer |
| 3 Summary | pressure above local tier | 9-class handoff (user verbatim, paths, errors, next) | folded tool process |
| 4 Rebuild | after 3 | **re-read** project rules + ≤5 cited files ≤5k tok each | stale citations |
| 5 Proactive / semantic | P1 | EWMA lookahead / topic shift | — |

Today: 0–3 exist. 4 does not (layers frozen at task start). 5 does not.
`go/contextengine` is identity; do not advertise it as a compressor.

Trigger: ~80% of (catalog window − reserve). Fallback window 32k is honest
only when the catalog has no window — do not silently use 32k for a 200k model.

After compact: constitution A–D unchanged; F/G **re-resolved** if the
operator is still in build/plan. Greeting turns still must not load F.

---

## 3. Token budget (operator-visible)

```
window     = catalog context or declared fallback
reserve    = max(4k, min(32k, window/10))
trigger    = 80% × (window − reserve)
pressure   = estimate(model_view) / trigger
warning    = 80%    critical = 90%
```

Replace `len/4+1` on Anthropic/OpenAI paths with provider usage when the
response includes it. Keep the estimator as fallback.

TUI `/context` already speaks VOICE. Do not show `ledger`/`hash` slang.
In-loop `tr.compact` and idle `/compact` must be labeled as different
jobs (live model view vs checkpoint rewrite).

---

## 4. Memory inject

| Stream | When | Budget | Where |
|--------|------|--------|-------|
| Project rules (F) | build/plan or repo-work heuristic | 8k chars, one winner per directory | Workspace layer |
| Governed snapshot (G) | task start, frozen | existing store cap | Workspace layer |
| HMS evidence | fresh task, hybrid mode | pinned observation | Transcript, not system |

Do not retrieve HMS or dump AGENTS.md for Fixture G.

---

## 5. Subagent contract (keep)

- Child: own session, attenuated profile, own transcript.
- Explore: no project rules, no writes, lean tools.
- Parent sees **only** `done.summary`.
- Do not copy parent TRANSCRIPT into the child.
- Do not copy child TRANSCRIPT into the parent.

---

## 6. Pipeline (as shipped)

```mermaid
flowchart TD
  start[Task start] --> snap[memory.snapshot]
  snap --> rules{build/plan or repo-work?}
  rules -->|yes| loadF[loadMemory 8k]
  rules -->|no greeting| skipF[skip AGENTS.md]
  loadF --> layers
  skipF --> layers[composeAgentPromptLayers once]
  layers --> loop[Each turn]
  loop --> cheap["compact(nil) elide/collapse"]
  cheap --> build[buildPromptSegmentsFromLayers]
  build --> full["seg.full = prefix + TASK + transcript + closing"]
  full --> route{Reasoner}
  route -->|Anthropic| cache[CacheSections A-D then workspace/catalog]
  route -->|Grok ACP| stuff["system = JSON ReAct; user = framed full()"]
  route -->|OpenAI| blob["single blob; cache kind none"]
  cache --> act[JSON action or native tools]
  stuff --> act
  blob --> act
  act --> exec[Kernel + Zig tools]
  exec --> obs[Observation snip 2k]
  obs --> after[model summary after turn]
  after --> loop
```

**P0 landed (0.8.31–0.8.34 + S8 + S7):** converse default, F mode-gated (not a phrase
classifier), TASK out of prefix, Grok ReAct envelope, compact summarizer off
the Think stack, capability brief out of live constitution, one-line builtins
with protocol C ≤ 5 (constitution without F < 800 tok), named A–D sections with
Anthropic `cache_control` per section. Remaining waste:
S10 REQUESTED skills in Catalog; `full()` rebuild every turn; no AGENTS
re-inject after compact.

---

## 7. P0 / P1 / P2

**P0** (prompt law `PROMPT_SPEC.md` S7–S11) plus:

| ID | Item |
|----|------|
| P0-C1 | Measure Fixture G and Fixture R input tokens / first 3 turns (constitution without F < 800 tok) |
| P0-C2 | Do not call compact a “context engine”; `go/contextengine` stays noop until it is not |

**P1**

| ID | Item |
|----|------|
| P1-C1 | Post-compact rebuild: re-read F + cited files (Claude rebuildAfterCompact job) |
| P1-C2 | Provider usage tokens instead of `len/4` when present |
| P1-C3 | Proactive compact (jcode EWMA) before hard 80% |
| P1-C4 | Structured tool-result extract for search/list (paths + 1-line why), not only snip |

**P2**

| ID | Item |
|----|------|
| P2-C1 | Semantic / topic-shift compact |
| P2-C2 | Memory retrieve budget per turn (jcode N→N+1) |
| P2-C3 | Tokenizer-accurate pressure in `/context` |

---

## 8. Anti-patterns

1. Do not compact the audit chain.
2. Do not fold user messages away without the verbatim budget.
3. Do not re-inject AGENTS.md into a greeting turn “because compact dropped it”.
4. Do not enable MiniLM / snapcompact as a default to look like OMP/jcode.
5. Do not return child transcripts to the parent.
6. Do not treat idle `/compact` as a substitute for in-loop `tr.compact`.
