# TUI Product UX Closure — Trade-offs and Plan

Date: 2026-07-16 (updated Wave H hygiene)  
Branch: `main`  
Sources: Grok Build (`xai-org/grok-build` user guide), Claude Code notes, OpenAI Codex (`codex-rs/tui/src/slash_command.rs`).

## Goal

Close the gap between Carina’s **governed runtime** and a **product-grade agent shell**, without weakening audit/profile boundaries.

## Competitive patterns (what we copy vs refuse)

| Pattern | Source | Copy? | Rationale |
|---------|--------|-------|-----------|
| Settings / extensions as modal | Grok `/settings`; CC LocalJSX `/config` | **Yes** | Inventory dump was the top complaint |
| Status line: model · mode · permissions · context% | Grok footer; Codex `/status` | **Yes** | Continuous “where am I?” |
| Shift+Tab mode cycle | Grok | **Partial** | Only **build↔plan**; never silent YOLO on cycle |
| Plan file + approve UI | Grok `plan.md` + `a` approve | **Partial** | `.carina/plans/<session>.md` + `/approve-plan`; no line-comment review UI |
| Skill as invocable slash | Grok + CC | **Yes** | Discoverability = execution |
| `/btw` side question | Codex Side/Btw; CC btw | **Partial** | Default answer-only; `/btw --fork` and `/side` call `session.fork` then switch session (no dual-pane) |
| `/commit` PromptCommand + git context | CC commit.ts | **Yes** | `workspace.diff` injected; commit-only rules |
| Extensions enable/disable | Grok extensions modal | **Partial** | `/extension enable\|disable` (admin-scope RPC) |
| Welcome / inspect readiness | Grok `/home`; CC doctor | **Yes** | `/inspect` `/welcome` |
| Permission modes (ask / dontAsk / bypass) | Grok/CC | **Yes (product HITL)** | `ask` \| `always-approve` \| `dont-ask` + org lock |
| Full marketplace / ACP / voice / silent YOLO | Grok/CC/Codex | **Defer** | Ecosystem / brand conflict |

## Trade-offs

1. **Side question vs side session**  
   Default `/btw` is an **answer-only** turn on the current session (honest copy).  
   `/btw --fork` / `/side` forks via `session.fork` and **switches** the TUI to the child session — not a dual-pane Side UI.

2. **Always-approve**  
   Grok’s bypassPermissions short-circuits prompts.  
   **Choice:** never silent YOLO; enable path always prints a WARNING; deny/plan/sandbox still apply; org may set `disable_always_approve`.

3. **Two approval axes (do not conflate names)**  
   - **Session/kernel:** `untrusted` \| `on_request` \| `never` on `session.create` / `InitSessionFull` — how the profile escalates or auto-allows at the kernel.  
   - **Product HITL:** `ask` \| `always-approve` \| `dont-ask` on daemon config / `/approval-mode` — what the daemon does when the kernel still returns `requires_approval`.  
   Session `never` is **not** a product-mode alias (rejected with an explicit error).

4. **Settings mutation depth**  
   Full in-panel TOML editing is multi-week.  
   **Choice:** settings shell + governed RPCs.

5. **Plan file location**  
   Grok uses `~/.grok/sessions/.../plan.md`.  
   **Choice:** workspace-scoped `.carina/plans/<session>.md`.

6. **Status refresh tick**  
   45s `tea.Tick` when attached; disabled under `testing.Testing()`.

## Wave map (status)

### Waves 1–3, A–D — **done**
Perception, workflow entry, extensions hub, semantic honesty, writable control, lifecycle, docs residual.

### Wave E — Context pressure + side fork — **done**
- Context pressure 80%/90%; auto-compact ≥85% only with paused checkpoint  
- `/btw --fork` and `/side` → `session.fork` then submit after attach  
- Busy-task fork refused with honest copy  

### Wave F — drift + always-approve — **done**
- `/always-approve` + WARNING; footer `ask` / `always-approve`  
- sticky `!` shell documented; agents humanized  

### Wave G — HITL taxonomy + org lock — **done**
- Product modes: `ask` \| `always-approve` \| `dont-ask`  
- `dont-ask`: deny without exact grant; no `permission.request`  
- `disable_always_approve` manage-lockable  
- Config/env/CLI: `approval_mode`, `CARINA_APPROVAL_MODE`, `-approval-mode`  

### Wave H — quality hygiene — **done**
- Closure plan + roadmap TUI section re-synced to E–G  
- Session-axis tokens rejected as product `approval_mode`  
- Dual-axis naming documented in README, enterprise, `/explain`  
- Working-tree hygiene: feature commits must not mix brand/CLI WIP  

### Wave I — WIP + product/i18n closure — **done**

- Free-text `ask_user` (omit options); structured still 2–6 options  
- Risk review outcome/risk/rationale visible in TUI transcript  
- README.zh-CN TUI/HITL/dual-axis + sticky shell + free-text  
- Local agent dirs (`.agents`/`.claude`/…) gitignored  
- Uncommitted clusters landed: TUI question keys/grapheme, `carina update`, brand  

## Still intentionally open

| Item | Why deferred |
|------|----------------|
| Mid-run auto-compact without paused checkpoint | Needs new daemon compact policy |
| Multi-pane dual-session TUI | Layout product; fork switches session today |
| Plan review overlay (a/s/c comments) | UX only; gate already hard |
| `acceptEdits` product mode | Optional Wave; capability whitelist |
| Prefix grants + dangerous list | Fatigue vs width trade-off |
| Subagent permission inheritance table | Swarm product contract |
| Hand-authored Traditional Chinese (non-derived) native review | Shipped as OpenCC-derived `zh-Hant`; native TW/HK editorial pass optional |
| ACP / remote marketplace / silent YOLO | Ecosystem / brand |
| IME human matrix (macOS Pinyin / fcitx5) | External terminal matrix |
| `apps/docs` Astro site | Scaffold untracked; separate productization |

## Acceptance (repository)

- [x] `go test ./go/tui/ ./go/daemon/ ./go/config/` green for HITL surfaces  
- [x] Footer shows `ask` \| `always-approve` \| `dont-ask`  
- [x] Session axis ≠ product axis (normalize rejects `never`/`on_request`/`untrusted`)  
- [x] This document matches shipped Wave E–G behavior  
