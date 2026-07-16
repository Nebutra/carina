# TUI Product UX Closure — Trade-offs and Plan

Date: 2026-07-16 (updated Wave M hygiene + prefix grants)  
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
| Plan file + approve UI | Grok `plan.md` + `a` approve | **Partial** | `.carina/plans/` + plan review overlay (`a`/`s`/`q`); no line-comment ranges |
| Skill as invocable slash | Grok + CC | **Yes** | Discoverability = execution |
| `/btw` side question | Codex Side/Btw; CC btw | **Partial** | Default answer-only; `/btw --fork` and `/side` call `session.fork` then switch session (no dual-pane) |
| `/commit` PromptCommand + git context | CC commit.ts | **Yes** | `workspace.diff` injected; commit-only rules |
| Extensions enable/disable | Grok extensions modal | **Partial** | `/extension enable\|disable` (admin-scope RPC) |
| Welcome / inspect readiness | Grok `/home`; CC doctor | **Yes** | `/inspect` `/welcome` |
| Permission modes (ask / dontAsk / bypass / acceptEdits) | Grok/CC | **Yes (product HITL)** | `ask` \| `always-approve` \| `dont-ask` \| `accept-edits` + org lock |
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
   - **Product HITL:** `ask` \| `always-approve` \| `dont-ask` \| `accept-edits` on daemon config / `/approval-mode` — what the daemon does when the kernel still returns `requires_approval`.  
   Session `never` is **not** a product-mode alias (rejected with an explicit error).

4. **Settings mutation depth**  
   Full in-panel TOML editing is multi-week.  
   **Choice:** settings shell + governed RPCs.

5. **Plan file location**  
   Grok uses `~/.grok/sessions/.../plan.md`.  
   **Choice:** workspace-scoped `.carina/plans/<session>.md`.

6. **Status refresh tick**  
   45s `tea.Tick` when attached; disabled under `testing.Testing()`.

7. **Approval grant width**  
   Exact resource match is the default. Session/project `FileRead`/`FileWrite` also install a **safe directory prefix** companion (not workspace-root, not dangerous paths). CommandExec stays exact-only for stored grants; a dangerous list refuses auto-reuse for high-blast-radius resources.

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
- `dont-ask`: deny without matching grant; no `permission.request`  
- `disable_always_approve` manage-lockable  
- Config/env/CLI: `approval_mode`, `CARINA_APPROVAL_MODE`, `-approval-mode`  

### Wave L — accept-edits + plan review overlay — **done**
- Product mode `accept-edits`: auto-allow `FileWrite`/`PatchApply` requires_approval; shell/network/secrets still prompt  
- `/accept-edits`, `/approval-mode accept-edits`, footer token  
- Plan review overlay via `/view-plan`: `a` approve, `s` request changes, `q` quit plan, esc close, j/k scroll  


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

### Wave J — Traditional Chinese (`zh-Hant`) — **done**

- Runtime key `zh-Hant` for `zh-Hant` / `zh-TW` / `zh-HK` / `zh-MO`  
- Catalogs derived from Simplified via OpenCC-compatible tables (`scripts/gen_zh_hant.py`)  
- TUI + microcopy pools + plural + locale resolution + docs  

### Wave K — quality guardrails — **done**

- `scripts/gen_zh_hant.py --check` + `make zh-hant-check` (stale Traditional table fails)  
- `make docs-build` (Astro/Starlight production smoke)  
- `make quality-check` aggregates brand + zh-hant + docs  
- CI job `quality-guardrails` runs zh-hant-check, brand-check, docs build  

### Wave M — hygiene + prefix grants + subagent contract — **done**

- Docs/protocol DRIFT closed: `rpc-catalog` re-synced from `methods.json`; enterprise, roadmap, policy.mdx, closure plan aligned to four product modes + Wave L  
- `/resume` vs `/task-resume` i18n copy aligned (compat alias kept)  
- Session/project `FileRead`/`FileWrite` install safe directory **prefix** companion grants; dangerous path/command list refuses grant auto-reuse  
- Subagent permission inheritance table (enterprise) + `TestSubagentPermissionInheritance`  


## Still intentionally open

| Item | Why deferred |
|------|----------------|
| Mid-run auto-compact without paused checkpoint | Needs new daemon compact policy |
| Multi-pane dual-session TUI | Layout product; fork switches session today |
| Plan line-range comments (Grok `c`) | Overlay has a/s/q only |
| Hand-authored Traditional Chinese (non-derived) native review | Shipped as OpenCC-derived `zh-Hant`; native TW/HK editorial pass optional |
| ACP / remote marketplace / silent YOLO | Ecosystem / brand |
| IME human matrix (macOS Pinyin / fcitx5) | External terminal matrix |

## Acceptance (repository)

- [x] `go test ./go/tui/ ./go/daemon/ ./go/config/` green for HITL surfaces  
- [x] Footer shows `ask` \| `always-approve` \| `dont-ask` \| `accept-edits`  
- [x] Session axis ≠ product axis (normalize rejects `never`/`on_request`/`untrusted`)  
- [x] Plan review overlay + accept-edits mode shipped (Wave L)  
- [x] Prefix grants + dangerous list + subagent inheritance contract (Wave M)  
- [x] Docs/catalog/i18n match shipped Wave E–M behavior  
