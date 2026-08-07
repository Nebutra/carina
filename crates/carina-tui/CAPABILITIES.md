# Carina TUI capability regression ledger

Issue **#25**. Authoritative inventory of user-visible TUI capabilities after the
Rust rewrite (`da6e4cf` and follow-on work). Update this file when shipping or
intentionally dropping a capability.

States:

| State | Meaning |
|-------|---------|
| **kept** | Present and covered by a test or snapshot |
| **wired** | Present in product path (may share tests with a parent surface) |
| **lost** | Previously present (Go TUI / prior audit) and not restored |
| **deferred** | Intentionally not shipped in this pass; reason recorded |
| **introduced-unwired** | Dependency/API present but call sites were zero (historical) |

## xai-ratatui-inline surfaces (historical six)

| Capability | State | Anchor | Notes |
|------------|-------|--------|-------|
| `insert_before` | **wired** | `app/mod.rs` scrollback commit path; `native_scrollback` tests | Finalized blocks commit via insert_before |
| `emit_to_scrollback` | **wired** | `app/mod.rs` raw/terminal wrap path | Used for Terminal wrap mode |
| `with_synchronized_output` | **wired** | `sync_output.rs`, `app/mod.rs` draw path | DECSET 2026; `CARINA_NO_SYNC_OUTPUT` / `CARINA_FORCE_SYNC_OUTPUT` |
| OSC 8 hyperlinks | **wired** | `hyperlink.rs` tests; issue #14 closed | Linkable targets detected and emitted |
| `diff_large` u16 fix | **kept** | `xai-ratatui-inline` tests | Engine-level; not reimplemented in carina-tui |
| `scrolling-regions` feature | **wired** | `carina-tui/Cargo.toml` enables feature | Preferred insert_before path |

## Content & discoverability

| Capability | State | Anchor | Issue |
|------------|-------|--------|-------|
| Golden-frame snapshots | **kept** | `tests/golden_frames.rs` | #2 |
| Render invariants | **kept** | `glyphs.rs`, `render_contract.rs`, `theme` contrast tests | #3 |
| Semantic design tokens | **kept** | `theme.rs`, `styles.md`, `style_discipline.rs` | #6 |
| Color capability degrade | **kept** | `theme.rs` ColorLevel | #7 |
| Light-background terminals | **kept** | `theme.rs` Polarity | #8 |
| Glyph vocabulary | **kept** | `glyphs.rs` | #9 |
| Layout constants | **kept** | `layout_contract.rs` | #10 |
| Live status cell | **kept** | `conversation.rs` ConversationStatus | #21 |
| i18n/width correctness | **kept** | `i18n.rs`, width helpers | #24 |
| Transcript typography | **wired** | `layout_contract` role prefix widths; `render.rs` hanging prefixes | #16 |
| Role labels + accents | **wired** | `theme::transcript_*`; role prefix tests in render | #17 |
| Syntax highlighting | **wired** | `syntax.rs` + markdown fenced code path | #18 |
| Diff line numbers + word-level | **wired** | `diff_render.rs` + tool Diff body | #19 |
| Tool intent + grouping + truncation hatch | **wired** | daemon `ToolCallRequested.intent`; `tool_projection` fallback; `transcript` group/expand tests | #20 |
| Semantic lifecycle + live/replay parity | **wired** | canonical protocol catalog; durable daemon event identity; `ExecutionLifecycleReducer`; transcript component-tree parity | ISSUE-002 |
| Typed failure recovery cells | **wired** | `FailurePresentation`; `execution.retry`; linked `retry_of_run_id`; reducer/action tests | ISSUE-014 |
| Installed skill slash discovery | **wired** | daemon skill prompt registry; trusted-root marketplace policy; slash command tests | ISSUE-016 |
| Subagent observation surface | **wired** | `agent.view` / `agent.recap`; retained Agents overlay; permission attenuation tests | ISSUE-012 |
| Governed plan-mode exit | **wired** | daemon plan tool mask; `session.approve_plan`; child plan inheritance and tool-matrix tests | ISSUE-009 |
| Dual-axis HITL footer | **wired** | typed `config.inventory`; persistent HITL/isolation rows; front-only approval queue count | ISSUE-010 |
| Model-aware context compaction | **wired** | catalog/fallback budget resolver; checkpoint snapshot; verifiable receipt policy metadata | ISSUE-004 |
| Context pressure and receipt detail | **wired** | persistent `ctx%`/`est.` footer; typed `/context` overlay; 80/90 semantic thresholds | ISSUE-011 |
| Patch attribution and rollback trust loop | **wired** | typed `workspace.patch.list/show/rollback.preview/rollback`; Changes workbench; apply/verify/rollback E2E | ISSUE-003 |
| Large diff progressive highlight + cap | **wired** | 2MiB/50k review cap; 500-row/32KiB transcript window; 4KiB line + bounded LCS; `/changes` paging | ISSUE-015 |
| ScreenMode + native scrollback | **wired** | Fullscreen default (alt-screen); Minimal opt-in native scrollback; Inline capability fallback; fenced re-exec handoff + exact commit watermark; status chip shows mode; forced Inline explains reason | ISSUE-005 / residual R5 |
| Follow-up queue inspect | **wired** | `/queue` overlay; `execution.queue.list` truncated preview; `execution.queue.drop`; durable depth footer | ISSUE-007 residual R7 |
| Tool row intent-first grammar | **wired** | Fixed 6-cell kind label; intent primary; dim technical target on single-tool rows; `format_tool_title` | V001 visual density |
| Success-read collapse language | **wired** | Title-only `Read ×N · path…`; expand restores members; fail/cancel never join success groups | V002 visual density |
| Semantic transcript cell skeletons | **wired** | Pure typed `SemanticCellKind`; production 120-column gallery; governance remains overlay-owned | V003 visual density |
| Visual density regression gate | **wired** | Production App matrix at 80/120/160 × EN/zh-Hans; Unicode style-run goldens; ASCII/color/polarity/selection contracts; Make + CI gate; persisted density axis joins in V004 | V011 visual density |
| Assistant content-type channel | **wired** | action JSON suppressed (`tool=done`→summary); whole-body JSON → pretty `json` markdown fence; else prose/markdown | transcript projection |
| Managed-proxy context window alias | **wired** | `ccswitch-*/model` resolves via openai/azure catalog; footer uses real window not 32k guess | daemon compaction_budget |
| Operator-facing reasoner failures | **wired** | degrade/ExecutionFailed reasons VOICE-ized; stacks stay on RoutingOutcome | daemon agent/reasoner |
| Edit dialogue-first density | **wired** | successful edits start collapsed; create-only card with path/stats/preview (no green + dump) | transcript + render |
| Session status labels | **wired** | `degraded`→partial; bare `unknown`→ready (not StatusUnknown spam) | conversation localization |
| Interactive final-turn prose contract | **wired** | `done.summary` must be plain language; no JSON completion payloads as user answer | daemon toolsHelp |
| Assistant phase duplicate sealing | **wired** | adjacent identical commentary is replaced by the authoritative final answer; transcript regressions | conversation projection |
| Key hints + real `/help` | **wired** | `help.rs`, `Overlay::Help`, `?` / `/help` | #22 |
| UI copy voice guide | **wired** | `VOICE.md` | #23 |
| Frame scheduling | **wired** | `frame_scheduler.rs` coalesce + idle `WaitPlan::Block` | #13 |
| Streaming feedback latency | **wired** | RPC receipt markers + five-phase `feedback_latency` stats + focus-gated 80 ms motion | ISSUE-008 |
| Sync output DECSET 2026 | **wired** | `sync_output.rs` | #12 |
| Resize reflow from source | **wired** | `TranscriptReflowState` + reflow_scrollback | #15 |
| Native scrollback migration | **wired** (partial) | ledger + insert_before path; default still uses product viewport where needed | #11 |
| Frame quality metrics | **wired** | `frame_quality.rs` | #5 |
| Agent bench harness | **wired** | `bench_harness.rs`, `make tui-bench` | #4 |

## Architecture constraints (executable)

1. **Color discipline** — prefer theme tokens; `tests/style_discipline.rs` guards raw palette sprawl.
2. **Glyph centralization** — product glyphs live in `glyphs.rs` with width invariants.
3. **Capability ledger** — this file; large TUI PRs should update the row state.

## Previously deferred — now shipped (late pass)

| Item | State | Anchor |
|------|-------|--------|
| Multi-terminal scrollback policy | **wired** | `native_scrollback::multi_terminal_policy_tests` |
| Wave 8 ExecutionRun (TUI/protocol) | **wired** | `execution.*`, legacy `task_id` reject, `TaskCreated` inert |
| Doctor recovery retained screen | **wired** | `doctor.rs`, `Overlay::Doctor`, `/doctor` |
| Inline media document layout | **wired** | `composer_document.rs` |
| syntect highlighter | **wired** | `syntax::highlight_syntect` + lightweight fallback |

Delegated agent-view work still uses `task_id` by design (not foreground ExecutionRun).

## Historical note

The Go TUI audit under `.trellis/tasks/archive/2026-07/07-24-source-backed-tui-audit/` used Bubble Tea acceptance criteria. Syntax highlighting (chroma) was lost in the Rust rewrite and is restored via syntect + `syntax.rs`.
