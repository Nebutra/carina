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
| Live status cell | **kept** | `conversation.rs` typed `ComposerChrome` projection | #21 |
| i18n/width correctness | **kept** | `i18n.rs`, width helpers | #24 |
| Transcript typography | **wired** | two-cell dialogue marks; `render.rs` hanging prefixes | #16 |
| Role labels + accents | **wired** | `theme::transcript_*`; speaker words stay out of the reading measure | #17 |
| Syntax highlighting | **wired** | `syntax.rs` + markdown fenced code path | #18 |
| Diff line numbers + word-level | **wired** | `diff_render.rs` + tool Diff body | #19 |
| Tool intent + grouping + truncation hatch | **wired** | daemon `ToolCallRequested.intent`; `tool_projection` fallback; `transcript` group/expand tests | #20 |
| Bounded evidence + full tool-output viewer | **wired** | visual-row head/receipt/tail budget; visible pointer/`Ctrl-T` entry; fixed owning-prompt band; paged artifact reads to 8 MiB; retained loading/failure state; unbounded logical scroll with EN/zh-Hans, Unicode/ASCII, short-terminal and resize regressions | runtime truth geometry |
| Semantic lifecycle + live/replay parity | **wired** | canonical protocol catalog; durable daemon event identity; `ExecutionLifecycleReducer`; transcript component-tree parity | ISSUE-002 |
| Model-aware failure recovery cells | **wired** | current/original `execution.retry` routing; immutable linked audit evidence; stable retry-chain projection; visible pointer/Tab actions | ISSUE-014 / 08-11 failure recovery UX |
| Installed skill slash discovery | **wired** | catalog lists `skill://name`; bodies load on `read` or slash, not stuffed into the prefix; trusted-root marketplace policy | ISSUE-016 |
| Operator `/new` `/fork` `/compact` `/goal` | **wired** | typed `CommandId` verbs over `session.create` / `session.fork` / `session.checkpoint.compact` / `goal.*`; `/help` lists the registry | operator command truth |
| Operator HITL + `/btw` | **wired** | `/always-approve` `/dont-ask` `/accept-edits` `/approval-mode` `/approve-plan` over `daemon.set_interactive_approval` / `session.approve_plan`; `/btw` answer-only via `execution.btw`; `/side` and `/btw --fork` withdrawn | operator command truth residual |
| Subagent observation surface | **wired** | `agent.view` / `agent.recap`; retained Agents overlay; permission attenuation tests | ISSUE-012 |
| Governed plan-mode exit | **wired** | daemon plan tool mask; `session.approve_plan`; child plan inheritance and tool-matrix tests | ISSUE-009 |
| Dense Plan Review | **wired** | retained numbered line/range comments; expected-run approval fence; `/view-plan`; shared footer/`/keymap` A/S/C/M/Q matrix; seven-locale outcomes and revision seed | V006 visual density |
| Dual-axis HITL state | **wired** | typed `config.inventory`; actionable governance remains visible while routine HITL/isolation facts live in Settings/Status; front-only approval queue count | ISSUE-010 |
| Model-aware context compaction | **wired** | catalog/fallback budget resolver; checkpoint snapshot; verifiable receipt policy metadata | ISSUE-004 |
| Context pressure and receipt detail | **wired** | warning/critical `ctx%` is protected in chrome; routine context lives in typed `/context`; 80/90 semantic thresholds | ISSUE-011 |
| Patch attribution and rollback trust loop | **wired** | typed `workspace.patch.list/show/rollback.preview/rollback`; Changes workbench; apply/verify/rollback E2E | ISSUE-003 |
| Fullscreen patch review workbench | **wired** | typed background `PatchReview` projection; transaction/file/hunk regions at 120/160; bounded numbered diff; exact preview identity; EN/zh-Hans goldens plus ASCII/no-color/resize geometry contracts | V005 visual density |
| Large diff progressive highlight + cap | **wired** | 2MiB/50k review cap; 500-row/32KiB transcript window; 4KiB line + bounded LCS; `/changes` paging | ISSUE-015 |
| ScreenMode + native scrollback | **wired** | Fullscreen default (alt-screen); Minimal opt-in native scrollback; Inline capability fallback; fenced re-exec handoff + exact commit watermark; Settings/Status shows mode; forced Inline explains reason | ISSUE-005 / residual R5 |
| Reconnect replay-tail + reading envelope | **wired** | Local TUI only: `event_replay_tail` v1 + watermarked `session.items`; atomic catch-up then live; `ReadingStateEnvelopeV1` restores block-ID selection, disclosure, follow-bottom, and logical line/sub-row across reconnect and ScreenMode. Local installed-prefix journey (`scripts/test-rust-tui-installed-reconnect.sh`) recorded 2026-08-14 via public `carina` + sibling `carina-ui`. Rollback remains `replayTailV1=false` if a future install regresses. | 08-12 reconnect replay |
| Follow-up queue inspect | **wired** | `/queue` overlay; `execution.queue.list` truncated preview; `execution.queue.drop`; durable depth footer | ISSUE-007 residual R7 |
| Tool row intent-first grammar | **wired** | Fixed 6-cell kind label; intent primary; dim technical target on single-tool rows; `format_tool_title` | V001 visual density |
| Success-read collapse language | **wired** | Title-only `Read ×N · path…`; expand restores members; fail/cancel never join success groups | V002 visual density |
| Semantic transcript cell skeletons | **wired** | Pure typed `SemanticCellKind`; production 120-column gallery; governance remains overlay-owned | V003 visual density |
| Persisted transcript density | **wired** | Compact default and Comfortable projection; `/density` + settings share one action; structured `tui_density` config; explicit disclosure wins; ScreenMode remains orthogonal | V004 visual density |
| Portable symbol tiers | **wired** | Automatic, Unicode, explicit Nerd Font, and ASCII preferences; live `/symbols` + Settings preview; width-locked semantic registry; structured `tui_glyphs` config; environment ownership disclosed | V008 visual density |
| Unified composer chrome | **wired** | State-derived zero/one/two-row `ComposerChrome` above a bottom-anchored composer; quiet idle, notice replacement, dual run/activity clocks, protected Queue/context warnings; `/queue` and pointer share `Action::OpenQueue`; 60/80/120/160 fallback geometry tests | V007 visual density |
| Runtime route + reading geometry truth | **wired** | typed `execution.status` + `ActiveRunPresentation`; Running/paused route separated from Next picker preference; centered `TranscriptGeometry` owns wrap/render/actions/wheel/scrollbar/page size at 120/160/180 with Unicode/ASCII parity | runtime truth geometry |
| Revisioned model preference | **wired** | `session.model.get/set` revision tuple; CAS conflict recovery; submit/retry preference fence; live-only typed change event that does not advance durable cursor; running route remains separate from Next preference | model preference concurrency |
| Visual density regression gate | **wired** | Production App matrix for both densities at 80/120/160 × EN/zh-Hans; Unicode style-run goldens; ASCII/color/polarity/selection contracts; Make + CI gate | V011 visual density |
| Assistant content-type channel | **wired** | action JSON suppressed (`tool=done`→summary); whole-body JSON → pretty `json` markdown fence; else prose/markdown | transcript projection |
| Managed-proxy context window alias | **wired** | `ccswitch-*/model` resolves via openai/azure catalog; footer uses real window not 32k guess | daemon compaction_budget |
| Operator-facing reasoner failures | **wired** | degrade/ExecutionFailed reasons VOICE-ized; stacks stay on RoutingOutcome; named recover `reason_code` on RoutingOutcome/ExecutionFailed | daemon agent/reasoner |
| Desktop OS notify | **wired** | unfocused live terminal/fail/degraded/needs-input only; replay skipped; `CARINA_DESKTOP_NOTIFY=off` kill switch | tui desktop_notify |
| OS sandbox honesty | **wired** | doctor reports requested/available/applied; missing sandbox-exec/bwrap fails closed | toolchain + carina-run |
| Spawn worktree isolation | **wired** | writable git spawn uses managed worktree; read-only stays shared; no second edit path | daemon spawn + worktree |
| Edit dialogue-first density | **wired** | `edit` and `patch` share `ToolKind::Patch`; successful edits start collapsed; create-only card with path/stats/preview (no green + dump) | transcript + render |
| Session status labels | **wired** | `degraded`→partial; bare `unknown`→ready (not StatusUnknown spam) | conversation localization |
| Interactive final-turn prose contract | **wired** | `done.summary` must be plain language; no JSON completion payloads as user answer | daemon toolsHelp |
| Assistant phase duplicate sealing | **wired** | adjacent identical commentary is replaced by the authoritative final answer; transcript regressions | conversation projection |
| Key hints + real `/help` | **wired** | `help.rs`, `Overlay::Help`, `?` / `/help` | #22 |
| UI copy voice guide | **wired** | `VOICE.md` | #23 |
| Frame scheduling | **wired** | `frame_scheduler.rs` coalesce; semantic Activity 33 ms / Status 80 ms demand; idle, unfocused, reduced-motion, and governance waits use `WaitPlan::Block` | #13 / V009 visual density |
| Streaming feedback latency | **wired** | RPC receipt markers + five-phase `feedback_latency` stats + focus-gated semantic motion | ISSUE-008 |
| Sync output DECSET 2026 | **wired** | `sync_output.rs` | #12 |
| Resize reflow from source | **wired** | `TranscriptReflowState` + reflow_scrollback | #15 |
| Native scrollback migration | **wired** (partial) | ledger + insert_before path; default still uses product viewport where needed | #11 |
| Frame quality metrics | **wired** | `frame_quality.rs` | #5 |
| Agent bench harness | **wired** | `bench_harness.rs`, `make tui-bench` | #4 |

## Architecture constraints (executable)

1. **Color discipline** — prefer theme tokens; `tests/style_discipline.rs` guards raw palette sprawl.
2. **Glyph centralization** — product glyphs live in `glyphs.rs` with Unicode,
   Nerd Font, and ASCII width invariants. `tui_glyphs` stores
   `auto|unicode|nerd|ascii`; Automatic never probes fonts or selects Nerd Font.
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
