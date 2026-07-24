# TUI Product UX Closure — Trade-offs and Plan

Date: 2026-07-16 (updated Wave N — remaining product open items)  
Branch: `main`  
Sources: Grok Build (`xai-org/grok-build` user guide), Claude Code notes, OpenAI Codex (`codex-rs/tui/src/slash_command.rs`).

> Evidence status: historical implementation plan. The cited sources were not
> pinned to exact revisions in this document. The comparison table records
> design provenance and choices made at the time; it is not current benchmark
> proof, visual parity evidence, or a completion score.

## Goal

Close the gap between Carina’s **governed runtime** and a **product-grade agent shell**, without weakening audit/profile boundaries.

## Competitive patterns (what we copy vs refuse)

| Pattern | Source | Copy? | Rationale |
|---------|--------|-------|-----------|
| Settings / extensions as modal | Grok `/settings`; CC LocalJSX `/config` | **Yes** | Inventory dump was the top complaint |
| Status line: model · mode · permissions · context% | Grok footer; Codex `/status` | **Yes** | Continuous “where am I?” |
| Shift+Tab mode cycle | Grok | **Partial** | Only **build↔plan**; never silent YOLO on cycle |
| Plan file + approve UI | Grok `plan.md` + `a` approve | **Yes** | `.carina/plans/` + plan review overlay (`a`/`s`/`q`/`c`/`m`); line-range comments seed request-changes |
| Skill as invocable slash | Grok + CC | **Yes** | Discoverability = execution |
| `/btw` side question | Codex Side/Btw; CC btw | **Yes** | Default answer-only; `/btw --fork` and `/side` fork + dual-pane (main snapshot \| side live); `/side-close` returns |
| `/commit` PromptCommand + git context | CC commit.ts | **Yes** | `workspace.diff` injected; commit-only rules |
| Extensions enable/disable | Grok extensions modal | **Partial** | `/extension enable\|disable` (admin-scope RPC) |
| Welcome / inspect readiness | Grok `/home`; CC doctor | **Yes** | `/inspect` `/welcome` |
| Permission modes (ask / dontAsk / bypass / acceptEdits) | Grok/CC | **Yes (product HITL)** | `ask` \| `always-approve` \| `dont-ask` \| `accept-edits` + org lock |
| Full marketplace / ACP / voice / silent YOLO | Grok/CC/Codex | **Defer** | Ecosystem / brand conflict |

## Trade-offs

1. **Side question vs side session**  
   Default `/btw` is an **answer-only** turn on the current session (honest copy).  
   `/btw --fork` / `/side` forks via `session.fork`, switches to the child, and shows a **dual-pane** when the terminal is wide enough: frozen main transcript snapshot on the left, live side session on the right. `/side-close` returns to the main session.

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

8. **Checkpoint compact**  
   Live agent loops already compact in-memory transcripts mid-run.  
   `session.checkpoint.compact` rewrites a **persisted** checkpoint at an **idle** task boundary (`paused` \| `completed` \| `failed` \| `degraded` \| `cancelled` \| `needs_input`), never while the session has an active run. Auto-compact at ≥85% uses this availability signal.

## Wave map (status)

### Waves 1–3, A–D — **done**
Perception, workflow entry, extensions hub, semantic honesty, writable control, lifecycle, docs residual.

### Wave E — Context pressure + side fork — **done**
- Context pressure 80%/90%; auto-compact ≥85% when compact.available  
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
- Closure plan + roadmap TUI section re-synced  
- Session-axis tokens rejected as product `approval_mode`  
- Dual-axis naming documented in README, enterprise, `/explain`  

### Wave I — WIP + product/i18n closure — **done**
- Free-text `ask_user`; risk review visibility; README.zh-CN; agent dirs gitignored  

### Wave J — Traditional Chinese (`zh-Hant`) — **done**
- Runtime key `zh-Hant`; OpenCC-derived catalogs; CI check  

### Wave K — quality guardrails — **done**
- `make quality-check` + CI `quality-guardrails`  

### Wave M — hygiene + prefix grants + subagent contract — **done**
- Docs/protocol DRIFT; path prefix grants + dangerous list; subagent inheritance  

### Wave N — remaining product open items — **done**
- Idle-boundary checkpoint compact (not only paused; still refuses mid-execution)  
- Plan line-range comments (`c` comment, `m` mark range → request-changes seed)  
- Dual-pane Side UI for `/side` and `/btw --fork` + `/side-close`  


## Intentionally out of repository closeout (skipped)

| Item | Why skipped |
|------|-------------|
| Hand-authored Traditional Chinese native review | Requires fluent human editorial pass (release evidence, not blocking) |
| IME human matrix (macOS Pinyin / fcitx5) | External terminal/hardware matrix |
| ACP / remote marketplace / silent YOLO | Ecosystem / brand non-goals |
| Apple signing, npm, Homebrew Core, VS Code Marketplace, … | External activation (roadmap) |

## Acceptance (repository)

- [x] `go test ./go/tui/ ./go/daemon/ ./go/config/` green for HITL / compact / plan / side surfaces  
- [x] Footer shows `ask` \| `always-approve` \| `dont-ask` \| `accept-edits`  
- [x] Session axis ≠ product axis  
- [x] Plan review overlay + line comments + accept-edits  
- [x] Prefix grants + dangerous list + subagent inheritance  
- [x] Idle-boundary compact + dual-pane side session  
- [x] This document matches shipped Wave E–N behavior  
