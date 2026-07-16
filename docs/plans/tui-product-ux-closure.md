# TUI Product UX Closure — Trade-offs and Plan

Date: 2026-07-16  
Branch: `main`  
Sources: Grok Build (`xai-org/grok-build` user guide), Claude Code notes, OpenAI Codex (`codex-rs/tui/src/slash_command.rs`).

## Goal

Close the gap between Carina’s **governed runtime** and a **product-grade agent shell**, without weakening audit/profile boundaries.

## Competitive patterns (what we copy vs refuse)

| Pattern | Source | Copy? | Rationale |
|---------|--------|-------|-----------|
| Settings / extensions as modal | Grok `/settings`; CC LocalJSX `/config` | **Yes** | Inventory dump was the top complaint |
| Status line: model · mode · permissions · context% | Grok footer; Codex `/status` | **Yes** | Continuous “where am I?” |
| Shift+Tab mode cycle | Grok | **Partial** | Only **build↔plan**; no silent always-approve |
| Plan file + approve UI | Grok `plan.md` + `a` approve | **Yes** | `.carina/plans/<session>.md` + `/approve-plan` |
| Skill as invocable slash | Grok + CC | **Yes** | Discoverability = execution |
| `/btw` side question | Codex Side/Btw; CC btw | **Honest partial** | Answer-only turn (no multi-session fork yet) |
| `/commit` PromptCommand + git context | CC commit.ts | **Yes** | `workspace.diff` injected; commit-only rules |
| Extensions enable/disable | Grok extensions modal | **Partial** | `/extension enable\|disable` (admin-scope RPC) |
| Welcome / inspect readiness | Grok `/home`; CC doctor | **Yes** | `/inspect` `/welcome` |
| Full marketplace / ACP / voice / YOLO | Grok/CC/Codex | **Defer** | Ecosystem / brand conflict |

## Trade-offs

1. **Side question without side session**  
   Codex/CC run a true side conversation or cache-safe parallel query. Carina TUI is still single-session.  
   **Choice:** `/btw` is an **answer-only** turn with explicit constraints and honest copy (“not a session fork”). Do not claim Side fork until multi-session UI exists.

2. **Always-approve**  
   Grok’s bypassPermissions short-circuits prompts.  
   **Choice:** refuse silent YOLO; expose sandbox/profile/approval via `/explain` and settings.

3. **Settings mutation depth**  
   Full in-panel TOML editing is multi-week.  
   **Choice:** settings shell + actions that call governed RPCs (`/approve-plan`, `/model`, extension toggle).

4. **Plan file location**  
   Grok uses `~/.grok/sessions/.../plan.md`.  
   **Choice:** workspace-scoped `.carina/plans/<session>.md` so plans travel with the repo and stay operator-visible.

5. **Status refresh tick**  
   Footer context goes stale without a poll.  
   **Choice:** 45s `tea.Tick` when attached; disabled under `testing.Testing()` so unit `drain` does not hang.

## Wave map (status)

### Wave 1 — Perception & control — **done**
Settings shell, humanized surfaces, mode cycle, compact UI, rich footer.

### Wave 2 — Workflow entry — **done**
`/btw` `/commit` `/init` `/remember` `/tasks` `/sessions` `/export` skill slash.

### Wave 3 — Extensions hub routes — **done**
Settings tabs; doctor/skills/hooks/mcp routes.

### Wave A — Semantic honesty — **done**
- Plan scaffold + `/view-plan` file preview  
- `/approve-plan` → `session.approve_plan`  
- `/commit` injects `workspace.diff`  
- `/btw` answer-only constraints + honest messaging  
- `/explain` sandbox vs plan mode  

### Wave B — Writable control — **done**
- Settings actions: approve plan, view plan, explain, inspect  
- `/extension enable|disable <name>` (admin RPC)  

### Wave C — Lifecycle — **done**
- Runtime status tick (45s)  
- `/tasks` + async schedule list  
- `/inspect` `/welcome` readiness aggregate  

### Wave D — Docs & residual — **done**
- This document  
- Explicit out-of-scope: ACP, marketplace, true side-fork, always-approve  

## Acceptance

- [x] `go test ./go/tui/` pass  
- [x] Plan file scaffold under `.carina/plans/`  
- [x] `/approve-plan` exits plan mode via daemon  
- [x] `/commit` uses workspace.diff  
- [x] `/btw` copy does not claim session fork  
- [x] `/explain` documents sandbox ≠ plan mode  
- [x] `/inspect` aggregates doctor + inventories  
- [x] Status tick does not hang unit tests  

## Still intentionally open

| Item | Why deferred |
|------|----------------|
| True side-session fork for `/btw` | Needs multi-session TUI |
| In-panel model list without leaving settings | Model picker already one keystroke |
| Auto-compact at 85% context | Requires safe mid-task compact policy |
| ACP / remote / marketplace | Ecosystem after shell stability |
| Always-approve mode | Governance brand |
