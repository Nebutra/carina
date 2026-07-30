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
| Tool grouping + truncation hatch | **wired** | `transcript` group; omission lines with expand key; truncated artifact copy | #20 |
| Key hints + real `/help` | **wired** | `help.rs`, `Overlay::Help`, `?` / `/help` | #22 |
| UI copy voice guide | **wired** | `VOICE.md` | #23 |
| Frame scheduling | **wired** | `frame_scheduler.rs` coalesce + idle `WaitPlan::Block` | #13 |
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
