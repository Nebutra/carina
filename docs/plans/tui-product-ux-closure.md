# TUI Product UX Closure — Trade-offs and Plan

Date: 2026-07-16  
Branch: `main` (direct maintenance)  
Sources: Grok Build user guide + public repo layout, Claude Code notes (`claude-code-notes`), OpenAI Codex TUI (`codex-rs/tui/src/slash_command.rs`).

## Goal

Close the gap between Carina’s **governed runtime** and a **product-grade agent shell**, without weakening audit/profile boundaries.

## Competitive patterns (what we copy vs refuse)

| Pattern | Source | Copy? | Rationale |
|---------|--------|-------|-----------|
| Settings / extensions as modal, not JSON dump | Grok `/settings` + extensions tabs; CC LocalJSX `/config` | **Yes** | Inventory dump is the #1 “can’t configure” complaint |
| Status line: model · mode · permissions · context% | Grok footer + Codex `/status` card | **Yes** | Continuously answers “where am I?” |
| Shift+Tab / mode cycle (plan/ask/always-approve) | Grok | **Partial** | We cycle **build↔plan** only; no silent always-approve without audit |
| Skill as invocable slash | Grok + CC | **Yes** | Discoverability must equal execution |
| `/btw` side question | Codex `Btw`/`Side`; Grok `/btw` | **Yes** | Non-interrupting operator question |
| `/commit` PromptCommand | CC | **Yes** | High-frequency git workflow |
| Transcript block fold defaults | Grok + existing Carina fold | **Keep** | Already default-collapsed for tool output |
| Full marketplace / ACP / voice / pets | Grok/CC/Codex | **Defer** | Ecosystem, not core coding loop |
| Always-approve YOLO default | Grok optional | **No** | Conflicts with Carina governance brand |

## Trade-offs

1. **Modal depth vs implementation cost**  
   Full multi-tab mutation UI (Grok extensions modal write path) is multi-week.  
   **Choice:** read-first settings shell + explicit action rows that route to existing governed commands (`/model`, `/mode`, `/keymap`, `/permissions new …`). Mutations still require deliberate commands.

2. **Permission modes**  
   Grok’s always-approve short-circuits prompts.  
   **Choice:** expose sandbox/profile/plan/interactive_approval as **visible status** and guided `/permissions` / `/mode`; do not add an unaudited yolo toggle in this pass.

3. **Operational surface noise**  
   CC/Codex avoid dumping raw config trees into chat.  
   **Choice:** human-first summaries for `context`/`config`/`permissions`/`skills`/`hooks`/`mcp`; full tree remains available under a short “details” section capped for readability.

4. **Dynamic skills**  
   True prompt expansion lives in daemon skill loading.  
   **Choice:** TUI resolves `skill.inventory` `user_invocable` names into `task.submit` with a skill-prefixed prompt (same path as custom commands), not a parallel agent runtime.

5. **i18n**  
   New chrome needs six locales.  
   **Choice:** catalog entries for all new MessageIDs; keep technical field keys English in detail sections.

## Wave map (this closure)

### Wave 1 — Perception & control
- Rich status footer (mode, model, effort, profile, sandbox, context, goal)
- Settings shell (`/settings`, `/config` opens shell)
- Humanized operational surfaces
- Mode cycle binding + `/plan`/`/build` aliases
- Compact UI mode

### Wave 2 — Workflow
- `/btw`, `/commit`, `/init`, `/remember`
- `/tasks`, `/sessions`, `/export`
- Skill invocable slash resolution
- `/view-plan` guidance surface

### Wave 3 — Polish / remaining parity
- Extensions hub routes (skills/hooks/mcp/extensions share settings shell tab)
- Doctor/inspect readiness lines in settings overview
- Tests locking behavior

Out of scope for this closeout: ACP transport, marketplace installs, remote bridge, voice, always-approve RPC.

## Acceptance

- `go test ./go/tui/ ./apps/carina-tui/ ./apps/carina-cli/` pass
- New commands appear in registry + help
- Status footer shows mode/model without requiring `/status`
- `/config` does not dump raw nested maps as the primary UX
- Skills marked `user_invocable` are executable via `/name`
