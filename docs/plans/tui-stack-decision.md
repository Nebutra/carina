# TUI Stack Decision

**Status:** Decision proposed, pending two time-boxed spikes (§4).
**Date:** 2026-07-09.
**Scope:** Which framework/language `apps/carina-tui` is built on, before it grows past its current 125-line stub (the P3.1 "architecture locked before line 126" checkpoint).
**Inputs:** `docs/plans/agent-cli-productization.md` (§P1, §P3, §4 microcopy, §6 brand questions), `docs/brand/brand-brief.md` (§2 palette/ANSI token table), a mid-2026 landscape survey of Rust TUI frameworks, an Ink/Claude-Code internals analysis, and a Carina integration audit (RPC contract, Go vs Rust client paths, layer-model boundaries).

**Provenance note:** landscape claims below (stars, release dates, issue numbers) come from a survey conducted against primary sources (repos, release pages, issue trackers) as of June–July 2026 and are reproduced here with their evidence markers. Claims are tagged **[verified]** (primary source seen in the survey) or **[claimed/unverified]** (inference or absence-of-evidence). Nothing in this document was answered from memory where the survey said evidence was missing — those gaps are listed as unknowns in §2.4 and become spike pass/fail criteria in §4.

---

## 0. Decision in one paragraph

**Build the TUI in Go on Bubble Tea v2 + lipgloss + bubbles (Charm stack), as `apps/carina-tui` in the existing Go module.** Runner-up: **Rust ratatui**, adopted only if the Go spike fails its zh-input or transcript-latency gates (§3.3). The user's starting hypothesis — the React model, ideally in Rust — does not survive the matrix: the only genuinely React-model Rust framework that is alive (iocraft) has open CJK text-input bugs and bus factor 1, which disqualifies it against Carina's zh-first-class requirement; and the requirements decomposition (§1.3) shows that what "React" actually buys is a rendering-pipeline checklist that Bubble Tea v2's new cell-diffing renderer satisfies natively, without a VDOM. Meanwhile the integration audit is lopsided: a Go TUI reuses `go/rpc`, the typed domain structs, socket discovery, and the P1.5/P3.1 shared data layer for free (~zero plumbing LoC); a Rust TUI must build the repo's first Rust JSON-RPC *client*, hand-maintain wire types in a second language forever, and either forfeit or forcibly relocate the plan-mandated shared renderer engine.

---

## 1. Requirements

### 1.1 Functional (from P1/P3)

| ID | Requirement | Source |
|----|-------------|--------|
| R1 | **Approval prompt whose body is a reviewable artifact**: colored unified diff for patch decisions, canonicalized command for exec, plan text for mode changes; 2–4 structured options (once/session/project/deny-with-reason); resolves `decision_id` over existing RPC (`task.approval.resolve` / `task.action.approve`) | P1.1 |
| R2 | **Streaming session transcript** with collapse/expand; entries with kernel verdict `read-only-allow` collapse by default (governance as visual hierarchy) | P3.1 |
| R3 | **Static/dynamic region split**: committed transcript lines flow to native terminal scrollback once; only the live region re-renders | Ink-lessons checklist (b)(f) |
| R4 | **Task progress pills** rendered from **server-side computed** pill segments (`/status` RPC) — client renders truth, doesn't compute it | P3.2 |
| R5 | **Audit views**: audit browser, pager for long replays | P3.1, P3.5 |
| R6 | **Degrade statuses end-to-end**: four-glyph vocabulary (`✓ ⚿ ✗ ~`) with ASCII/NO_COLOR fallbacks; distinct microcopy per initiator | P1.3 |
| R7 | **Degrade to plain CLI / `--json` parity**: TUI is a skin over the same daemon RPC data layer as the CLI renderer — "one engine, two skins" | P1.5, P3.1 (hard architectural mandate) |
| R8 | **Reconnect state machine**: exponential backoff, permanent-failure classification for policy denials, >60s gap resets retry budget | P3.3 |
| R9 | **First-paint discipline**: input frame paints first; index warmup/git status/audit-head verify load in background; stdin buffered from process start and replayed | P3.1 |
| R10 | **Notifications**: terminal bell + OSC 9/777 when a background session blocks on approval | P3.5 |

### 1.2 zh as a first-class locale

| ID | Requirement |
|----|-------------|
| R11 | All width math via grapheme clustering + east-asian-width (CJK = 2 columns) — layout, wrapping, and **cursor column computation in text inputs** |
| R12 | Committed multi-byte input inserts/deletes/backspaces by grapheme, not byte or rune |
| R13 | Physical cursor kept exactly at the logical caret cell, so the terminal places the IME candidate window correctly (this is the industry failure mode: bubbletea #874, Claude Code #19207/#22732) |
| R14 | Bracketed paste with paste-vs-typing detection and `[Pasted X lines]` collapse |
| R15 | Accept as industry reality: in-TUI composition preview is unsolved everywhere (Ink, Claude Code, Bubble Tea all fail it). Mitigation is R13, not a framework feature. Test matrix: macOS Pinyin, fcitx5/Wayland, Windows Terminal + MS Pinyin |

### 1.3 The React model, decomposed (what the user actually needs)

From the Claude Code Ink-internals analysis: "React" is one implementation of five properties, and Claude Code had to *fork Ink* and add compiler memoization + context splitting to keep them. The transferable requirements are:

| ID | Requirement | Note |
|----|-------------|------|
| R16 | UI = f(state): declarative frame description, no manual escape-sequence bookkeeping | Satisfiable in Elm, immediate-mode, or retained styles |
| R17 | Minimal steady-state output: double-buffered screen with cell/row-level delta, never full repaint; correctness escape hatches (resize, SIGCONT, occlusion → force full repaint) | Bubble Tea v2 "Cursed Renderer" does this at framework layer **[verified: v2 release]**; Ink needed Claude Code's in-tree rewrite to do it well |
| R18 | Per-line/segment render caching so a huge transcript costs O(changed rows) | Ink charCache lesson; manual in Go (string builders cached per entry), manual in ratatui too |
| R19 | Component-scoped, independently testable units; update-frequency isolation (a spinner tick must not re-render the tree) | Elm gives this via sub-model discipline, not for free |
| R20 | Priority-aware input: keystroke echo preempts expensive derived work | Event-loop architecture question, not framework feature |
| R21 | Single-cursor arbitration ("declared cursor"): topmost active input owns the physical cursor; approval dialog overlaying the prompt steals it | Directly serves R13 |
| R22 | Repaint attribution/observability from day one (which region caused the full repaint?) | Claude Code's `CLAUDE_CODE_DEBUG_REPAINTS` lesson |

**Anti-requirements** (where React *hurt* Claude Code): cascading re-renders needing compiler memoization; VDOM + Yoga + WASM as a heavy dependency chain for a character grid; blit-correctness dirty-pixel bugs. The industry convergence in 2025–26 (Bubble Tea v2 Cursed Renderer, opencode's Zig-core OpenTUI) is *cell-diff renderer + declarative view, without React itself*.

### 1.4 Hard constraints

| ID | Constraint |
|----|-----------|
| C1 | **No Node.js at runtime** — CI has a dedicated `no-node-runtime` job (`.github/workflows/ci.yml:60`). Disqualifies Ink and OpenTUI (Bun/TS runtime) outright. |
| C2 | **P3.1 shared data layer**: every TUI view is a component over a daemon RPC *shared with the Go CLI renderer* (extends P1.5). A different-language TUI either duplicates or relocates this layer. |
| C3 | **Theming from the brand token table**: `docs/brand/brand-brief.md` §2 — truecolor hex + 256-color fallbacks + mono; semantic ANSI mapping (error→Crimson/132, warning→Star Gold/137, success→Core Glow/139, info→Blue Giant/189, muted→Dust Mauve/96). One palette table, NO_COLOR honored (P3.5). |
| C4 | **Microcopy engine consumption**: Governed/Degrade registers from `go/microcopy` (P1.7) — a Go package. A non-Go TUI must re-implement the FNV-1a seeded deterministic pick + suppression rules or call through RPC. |
| C5 | Distribution through the existing tarball/homebrew pipeline (`scripts/package-release.sh` already stages `bin/carina-tui`). |

---

## 2. Candidates and evaluation matrix

### 2.1 Candidate profiles (evidence summary, mid-2026)

- **Bubble Tea v2 + lipgloss + bubbles (Go).** Elm architecture (Model/Update/View), *not* React. v2.0.0 released 2026-02-23 — first breaking release in six years — with a declarative `tea.View` struct and the "Cursed Renderer" (ncurses-style cell-level diffing), i.e. the framework independently converged on Ink's double-buffer + minimal-delta pipeline **[verified]**. v2.0.8 on 2026-07-03; active monthly cadence; maintained by Charm Inc. (VC-funded, multiple paid maintainers) **[verified]**. CJK width: solid via x/ansi + rivo/uniseg grapheme clustering; bubbles v2 textinput/textarea are wide-char-aware **[verified per survey]**. IME: issue #874 (candidate window misplaced, Linux/fcitx5) open since Nov 2023, milestoned to v2.0.0, **still open** at survey time **[verified]** — the exact R13 risk. No flexbox; layout is lipgloss joins + manual width math. Flagship precedent: **charmbracelet/crush**, a full agentic coding TUI (streaming transcripts, tool-approval dialogs, diff rendering, session switching) built by the framework's own maintainers — the closest existing analogue to Carina's TUI **[verified]**. Counter-signal: SST's opencode v1.0 *left* Bubble Tea for a TS+Zig stack explicitly to get a React/Solid component model **[verified]**.
- **ratatui (+ ecosystem) (Rust).** Immediate-mode; you own the event loop. The anchor of the Rust TUI world: 21.5k stars, ~36M downloads, 4600+ dependent crates, org-maintained (multiple maintainers — best maintenance story surveyed); v0.30.1 stable June 5, 2026 **[verified]**. **Strongest CJK evidence of any candidate**: unicode-width handling is an actively managed maintainer concern (deliberate 0.2.0 pin after the width-table controversy, discussion #1438); `Frame::set_cursor_position` gives the correct terminal-IME architecture for R13; and the killer field datum — **OpenAI Codex CLI, a governed-agent TUI with approval prompts and streaming transcripts, ships on ratatui and received CJK word-navigation fixes in production (openai/codex PR #16829)** — while Ink-based Claude Code has the open IME bug **[verified]**. Richest widget ecosystem: ratatui-textarea (unicode-aware editor), ansi-to-tui (render `delta`/git colored diffs directly), syntect-tui, ratkit (code-diff widget), viewports, gauges. Not the React model — TEA-style state struct or a hand-rolled signal layer.
- **iocraft (Rust).** The genuine React model in Rust: `element!` macro, `#[component]`, hooks, flexbox via taffy; explicitly Ink-inspired. ~1.4k stars, v0.8.3 May 2026, responsive triage — but **single maintainer (bus factor 1)** and 0.x API churn **[verified]**. **Disqualifying for zh today**: open issue #208 (June 19, 2026) — cursor position/indexing errors in TextInput with mixed non-English/English text — is precisely the wide-char cursor math R11 needs, currently broken **[verified]**. No documented east-asian-width strategy, no IME cursor handling, no diff/scrollback/textarea widgets, no ratatui interop. Shipped products (moonrepo proto/moon) are styled console output, not interactive fullscreen TUIs **[verified]**.
- **rooibos (Rust).** Leptos-style signals over ratatui — conceptually ideal (React DX + ratatui widgets) but **pre-alpha by its own README, ~5 stars, single author** **[verified]**. Not adoptable; useful only as a design reference for an in-house signal layer.
- **tui-realm (Rust).** Elm/React hybrid over ratatui; alive (4.1.0 May 2026, tracks ratatui 0.30) but single-maintainer, message-plumbing-heavy, and the model is Elm anyway — it adds a dependency without adding the React model. No CJK evidence either way **[absence of evidence, not correctness]**.
- **dioxus-tui / rink (Rust).** Effectively abandoned: last publish ~2024, removed from Dioxus main branch as of v0.5, frozen on unmaintained tui-rs **[verified]**. Do not adopt.
- **Ink (TypeScript)** and **OpenTUI (Zig core + TS/React bindings, Bun runtime).** Both hard-disqualified by C1. Ink's role in this document is as a *requirements source* (§1.3): the decisive datapoint is that Anthropic could not ship Claude Code on stock Ink — it vendors and deeply forks it (custom reconciler, double-buffer, charCache, virtual scroll, declared cursor) **[verified per internals analysis]**. OpenTUI confirms the survey question "anything newer that is React-for-terminal?": yes, but it's TS-over-native — no new mature Rust React-model entrant appeared in 2025–26.

### 2.2 Integration cost (from the repo audit — all [verified] against source)

The wire protocol is deliberately language-neutral: NDJSON JSON-RPC 2.0 over `~/.carina/daemon.sock`, machine-readable contract in `protocol/jsonrpc/methods.json` + `protocol/schemas/*.json`, approval flow fully wire-visible (`permission.request` events carrying `decision_id`; `go/daemon/approval.go:57-69`). A Rust client *can* drive everything today. But the costs diverge:

| | Go path | Rust path |
|---|---|---|
| RPC client | `go/rpc.Client` exists (149 LoC, used by CLI/SDK/stub) | Build the repo's **first Rust JSON-RPC client** (~300–500 LoC: unix dial, correlation, interleaved-notification demux, second streaming connection) |
| Typed wire models | Same Go module — struct tags already match the wire; zero re-derivation | Hand-write ~500–1000 LoC of structs for Session/Event/PermissionDecision/Task/PatchTransaction; no codegen exists; **permanent drift surface** against Go structs |
| P1.5/P3.1 shared engine (C2) | Shared by construction | Duplicate (~1–2k LoC, two-language drift) or migrate the CLI renderer to Rust too |
| Microcopy engine (C4) | Import `go/microcopy` | Re-implement or RPC round-trip |
| Build/packaging | Already done (Makefile `go:` target, `package-release.sh:153` stages `bin/carina-tui`) | New workspace member + release build + copy line (precedent exists via carina-kernel-service; ~small) |
| Pre-first-view plumbing | **≈ 0 LoC** | **≈ 1.5–3k LoC** |
| Layer-model fit | Control plane + surface = Go (documented) | Surface is marked "TypeScript (initially)" — provisional, so a Rust surface is a doc amendment, not a contract breach; but Rust's documented job is the kernel. Would be the first Rust-as-client of the daemon. |
| SDK state | sdk/go wraps `go/rpc` today | No Rust SDK exists; a Rust TUI creates the fourth SDK-grade client and immediately needs Phase-1 features (streams, approvals) no SDK has reached |

### 2.3 Matrix

Scoring: ✓✓ strong/verified · ✓ adequate · ~ achievable with work · ✗ failing/unknown-risk. **Bold** rows are gating.

| Criterion | Bubble Tea v2 (Go) | ratatui (Rust) | iocraft (Rust) | tui-realm (Rust) | rooibos (Rust) | dioxus-tui | Ink/OpenTUI |
|---|---|---|---|---|---|---|---|
| **C1 no Node runtime** | ✓✓ | ✓✓ | ✓✓ | ✓✓ | ✓✓ | ✓✓ | **✗ disqualified** |
| **R11–R12 CJK width/grapheme** | ✓✓ verified | ✓✓ strongest (Codex CLI in production) | ✗ open bugs #206/#208 | ~ unverified | ~ inherits ratatui render; inputs unproven | ✗ frozen on tui-rs | — |
| **R13 IME cursor anchoring** | ~ #874 open; needs cursor-position reporting — **spike gate** | ✓ `Frame::set_cursor_position` is the correct architecture; field-proven via Codex CLI | ✗ undocumented | ~ unknown | ✗ unknown | ✗ | — |
| R1 diff-body approval prompt | ~ no first-party diff widget; Crush's chroma+custom diff is copyable prior art | ✓✓ ansi-to-tui + syntect-tui + ratkit exist as maintained crates | ✗ DIY from primitives | ✓ wraps ratatui widgets | ✓ (theoretically) | ✗ | — |
| R2–R3 transcript scrollback, static/dynamic split | ✓ viewport + list composition (Crush, gh-dash precedent); transcript-scale string caching is manual — **spike gate** | ✓ viewport widgets; per-frame caching also manual | ✗ no scrollback widget | ✓ via ratatui | ? | ✗ | — |
| R16 declarative UI=f(state) | ✓ (View() explicit) | ✓ (redraw on state change) | ✓✓ (hooks, closest to Ink) | ✓ | ✓✓ | ✓✓ | ✓✓ |
| R17 cell-diff minimal output | ✓✓ Cursed Renderer, framework-level | ✓✓ double-buffer diffing built in | ✓ | ✓ (via ratatui) | ✓ (via ratatui) | ✗ | ✓ (forked) |
| R19 component isolation / hooks feel | ~ Elm sub-model discipline; message-routing tax is real (Crush's tui package is big and manually wired) | ~ TEA-style or in-house signals; most boilerplate | ✓✓ | ✓ | ✓✓ | ✓✓ | ✓✓ |
| **C2 shared data layer with Go CLI** | ✓✓ by construction | ✗ duplicate or migrate | ✗ same | ✗ same | ✗ same | ✗ | ✗ |
| C4 microcopy engine | ✓✓ direct import | ~ RPC or re-implement | ~ | ~ | ~ | ~ | — |
| Maintenance reality | ✓✓ Charm Inc., v2.0.8 Jul 2026 | ✓✓ org-maintained, best in survey | ✗ bus factor 1, 0.x | ~ solo but long track record | ✗ pre-alpha | ✗ abandoned | ✓ (Ink solo; CC fork in-house) |
| Product precedent for *this* kind of app | ✓✓ Crush (agentic TUI w/ approvals, by the maintainers) | ✓✓ Codex CLI (governed-agent TUI, CJK-fixed in prod) | ~ styled output only | ~ termscp | ✗ none | ✗ none | ✓✓ Claude Code (but forked engine) |
| Team-stack coherence (Go daemon owns RPC types) | ✓✓ | ✗ second wire-type language forever | ✗ | ✗ | ✗ | ✗ | ✗ |
| Pre-first-view plumbing cost | ✓✓ ≈0 | ✗ 1.5–3k LoC | ✗ same + widgets DIY | ✗ same | ✗ same | — | — |

### 2.4 Honest unknowns (become spike gates)

1. **Bubble Tea R13**: does #874's failure mode (IME candidate window misplaced) bite on macOS Pinyin and fcitx5 for Carina's inline prompt, and can cursor-position reporting be emitted from the v2 renderer without upstream changes? *Unverified — no one in the survey tested Carina's exact shape.*
2. **Bubble Tea transcript scale**: v2's Cursed Renderer diffs cells, but Go-side `View()` string building for a multi-thousand-line transcript needs manual per-entry caching (the Ink charCache lesson). Achievable, but latency under streaming load is unmeasured.
3. **ratatui zh input**: ratatui-textarea's CJK cursor math is well-evidenced for rendering; Carina-shaped IME behavior on the three-platform matrix is still assumed from Codex CLI's precedent, not tested here.
4. **tui-realm / rooibos CJK input**: no evidence either way; not pursued because both fail other gates.
5. Web access was not re-exercised for this document; all external claims carry the survey's as-of dates (June–July 2026). If any load-bearing claim (esp. #874 status, iocraft #208 status) must be current at spike time, re-check the trackers first.

---

## 3. Recommendation

### 3.1 Primary: Bubble Tea v2 + lipgloss + bubbles (Go)

The matrix is decisive on three gating rows: C2 (P3.1's shared-data-layer mandate is only free in Go), team-stack coherence (the daemon, RPC types, microcopy engine, CLI renderer are all Go), and plumbing cost (≈0 vs 1.5–3k LoC plus permanent two-language wire-type drift). On the requirement the user cares most about — web-grade interaction feel — the decomposition in §1.3 shows the load-bearing parts are the rendering pipeline (R17–R18) and component isolation (R19), and Bubble Tea v2's Cursed Renderer ships R17 at the framework layer, something stock Ink never had (Claude Code built it in-tree). The Elm model is a real tax (message routing, no hooks), but Crush proves Claude-Code-grade agentic UX — streaming transcripts, approval dialogs, diff views — is shippable on exactly this stack by a team smaller than the problem.

**Mitigations for the known weaknesses:**
- *Elm plumbing tax*: adopt a strict component convention from day one — each view is a sub-model with `Init/Update/View` and its own message namespace, mirroring bubbles' convention; P3.2's server-side pill computation and P1.5's shared data layer already push logic out of the client, shrinking what Elm has to route.
- *No flexbox*: Carina's P1/P3 views are lists, panes, and dialogs — lipgloss joins + width math suffice; Claude Code-style absolute overlays exist in v2 layers (young — exercise in spike).
- *Diff widget*: port Crush's chroma + unified/side-by-side diff renderer pattern (in-repo, copyable prior art) and feed it P1.1's reviewable-artifact payloads.
- *R13 IME*: the spike's zh gate (§4.1) decides; the mitigation architecture (single declared cursor, R21, physical cursor pinned to caret) is framework-independent.
- *R18 caching*: per-transcript-entry rendered-string cache keyed on entry revision — the charCache lesson, implemented as a Go map; measure in spike.

### 3.2 Runner-up: ratatui (Rust)

If Go fails its gates, ratatui — not iocraft — is the Rust choice: best maintenance story in the survey, strongest CJK evidence (Codex CLI in production is the closest existing product to Carina's TUI requirements), correct IME cursor architecture, and every P1/P3 widget need (colored diff via ansi-to-tui, textarea, viewport, gauges) exists as a maintained crate. The costs are accepted, not wished away: the first Rust RPC client, hand-written wire types with drift risk, and a P3.1 amendment (either the shared engine is duplicated, or the CLI renderer migrates to Rust too — the honest version of "Rust TUI" is "Rust surface layer", a docs/architecture.md amendment the "TypeScript (initially)" wording permits).

**iocraft is explicitly rejected for now** despite being the truest React-in-Rust: open CJK TextInput cursor bugs (#208, June 2026) fail R11 outright, bus factor 1, no diff/scrollback widgets, no ratatui interop. Reversal condition in §3.3. rooibos is pre-alpha (design reference only); tui-realm adds Elm-with-extra-steps; dioxus-tui is dead.

### 3.3 Reversal triggers

Choose Bubble Tea v2 **unless**:
1. **zh gate fails**: the Go spike cannot get the IME candidate window anchored at the caret (macOS Pinyin + fcitx5) with committed-text grapheme correctness, *and* the ratatui spike passes the same test — then adopt ratatui and schedule the P1.5 engine question as a follow-up decision.
2. **Latency gate fails**: streaming a real session transcript (≥2k lines, 30 events/sec) exceeds p95 16ms redraw or >2% idle CPU after caching mitigations — and ratatui's spike meets it.
3. **Upstream regression**: Charm abandons v2 cadence or #874 is closed as won't-fix with no cursor-reporting workaround (re-check tracker at spike time).

Revisit **iocraft** (not adopt) only if all of: #208/#206-class CJK input bugs are fixed upstream, a second maintainer lands, and a diff/scrollback story exists — reassess at the next TUI milestone, not before.

### 3.4 Hybrid honestly considered — and rejected

- *Go CLI stays + Rust TUI*: pays the full Rust plumbing cost **and** breaks C2 (two data layers, or a Rust engine the Go CLI can't share). Worst of both. Rejected.
- *Everything-Rust surface* (CLI renderer migrates too): coherent but is a re-platforming project stapled to a TUI project; the CLI (P1.x) ships first and is already Go. Only sensible as a deliberate later migration if trigger 1/2 fires.
- *Go TUI + Rust compute over RPC*: already the architecture — the kernel (policy, audit, index) is Rust behind the daemon. The TUI is a thin skin by mandate (R4, R7); there is no TUI-side compute that wants Rust.
- *OpenTUI-style TS-over-native*: C1 kills it regardless of merit.

---

## 4. Spike plan (1–2 days each, run in parallel)

Both spikes implement the **same script** against the **real daemon** (no mocks): connect to `~/.carina/daemon.sock` on two connections (calls + event stream, per `go/rpc/client.go`'s demux pattern), `session.attach` + `session.events.stream`, render a live streaming transcript, and when a `permission.request` event arrives, render an approval prompt whose body is a colored unified diff, resolve it via `task.approval.resolve {decision_id}`.

### 4.1 Common pass/fail gates

| # | Gate | Pass criterion |
|---|------|----------------|
| G1 | Live approval prompt | `permission.request` → prompt with 4 options + colored diff body (use a real `patch-transaction` payload); approve/deny round-trips `decision_id`; Escape always closes (never locks cursor) |
| G2 | Streaming transcript | 2,000-line transcript, 30 events/sec injected: collapse/expand a read-only-allow entry works mid-stream; no visible tearing; committed lines land in native scrollback (static/dynamic split, R3) |
| G3 | zh input — width | Type `carina 审批测试 with mixed 中英 text` into the input: cursor lands on correct cells throughout; backspace deletes by grapheme; paste of 10 CJK lines triggers bracketed-paste collapse |
| G4 | zh input — IME | macOS Pinyin **and** fcitx5 (Linux VM): candidate window appears at the caret cell, not at 0,0 or a stale position; committed text inserts correctly. (In-composition echo inside the TUI is *not* required — R15.) |
| G5 | Idle CPU | < 1% of one core with a spinner + pill visible, measured over 60s (`ps`/`top` sampling) |
| G6 | Redraw latency | p95 keystroke-echo < 16ms and p95 event-to-paint < 33ms under G2 load (instrument with timestamps in the render loop — R22 observability starts here) |
| G7 | Degrade | `--json`-mode invocation of the same data layer emits schema-versioned frames incl. `control_request{decision_id}` (P1.5); NO_COLOR renders the four-glyph ASCII fallbacks |
| G8 | Brand tokens | Palette table from brand-brief §2 loaded from one Go/Rust constants file; truecolor + 256 fallback verified in Terminal.app and a 256-color tmux |

### 4.2 Go spike specifics

- Stack: `charm.land/bubbletea/v2`, lipgloss, bubbles v2 (textarea, viewport, spinner); diff via chroma + a minimal unified renderer (crib Crush's approach).
- Extra gate **G9-go**: per-entry rendered-string cache demonstrates O(changed rows) — instrument `View()` cost with and without cache at 2k entries.
- Extra gate **G10-go**: emit cursor-position so G4 passes; if #874's failure mode appears, attempt the workaround (position hardware cursor via v2 cursor API at the caret each frame) and record whether it requires upstream patching.

### 4.3 Rust spike specifics

- Stack: ratatui 0.30, crossterm, ratatui-textarea, ansi-to-tui (feed it `git diff --color` output directly); NDJSON JSON-RPC client over `std::os::unix::net` (~300 LoC, serde_json).
- Extra gate **G9-rs**: count plumbing LoC honestly (client + hand-written types for the ~10 methods used) — this number feeds the final decision if triggers fire.
- Extra gate **G10-rs**: `Frame::set_cursor_position` anchored at the textarea caret passes G4 on both platforms.

**Decision rule:** if both pass all gates → Go wins on §2.3's gating rows. If Go fails G4 or G6 and Rust passes → reversal trigger 1/2, ratatui wins and a P3.1 amendment is drafted. If both fail G4 → the failure is terminal-side; pick Go and ship R21 mitigations, tracking upstream.

---

## 5. Productization path (Go winner)

### 5.1 Layout

```
apps/carina-tui/            # grows from the 125-line stub; bin target unchanged
  main.go                   # flags, locale/isatty detection, tea.NewProgram
  app/                      # root model: routing, layers/overlays, declared-cursor registry (R21)
  views/                    # one package per P3.1 view, in plan order:
    approvals/              #   P1.1 data — first view
    patchreview/            #   diff viewer (shared diff renderer below)
    sessions/               #   picker/graph (P2.2 data)
    audit/                  #   audit browser + pager
  components/               # pill, statusglyphs, transcript (collapse/expand + entry cache), input
  theme/                    # brand token table (§5.2)
go/tuiengine/  (or extend the P1.5 engine package)   # the "one engine": typed view-models
                            # computed from RPC results, consumed by BOTH the CLI renderer
                            # and apps/carina-tui — this package is the C2 contract, no
                            # lipgloss import allowed here
go/rpc/                     # unchanged; TUI uses two connections (calls + event stream)
```

Dependency delta: Bubble Tea v2 + lipgloss + bubbles + chroma in the root `go.mod` — the module's first third-party UI deps; pure Go, cross-compiles in the existing pipeline, `no-node-runtime` CI unaffected.

### 5.2 Brand tokens (C3)

`apps/carina-tui/theme/tokens.go` transcribes brand-brief §2 verbatim: each token = `{name, truecolor hex, ansi256 fallback, mono fallback}`; semantic map (error→Carina Crimson/132, warning→Star Gold/137, success→Core Glow/139, info→Blue Giant/189, muted→Dust Mauve/96) is the only way views obtain color — no hardcoded ANSI (R22's Ink lesson). lipgloss `AdaptiveColor`/profile detection handles truecolor→256→mono; NO_COLOR and `--plain` collapse to the four-glyph ASCII set (P1.3, brand §6 Q2). When the brand kit ships its TUI theme token table file, `tokens.go` becomes generated-from or checked-against it.

### 5.3 Microcopy engine (C4)

Direct import of `go/microcopy` (P1.7): Governed register strings for approval prompts (`decision_id` asks), Degrade register for status labels, Ambient register for spinners at P3.4. Locale resolution (`--locale` > env > `en`; zh first-class) happens once at TUI init and is passed to both microcopy and the theme layer. Suppression rules (`--json/--plain/!isatty`) already live in the engine — the TUI adds nothing.

### 5.4 Delivery sequencing (aligns to the plan)

1. **After P1.1/P1.5 land** (approval data + engine exist): spike hardens into `views/approvals` — first shippable TUI view.
2. P3.1 cluster: first-paint discipline (R9), stdin buffering, verdict-collapsed transcript.
3. P3.2 pill (server-side segments), P3.3 reconnect state machine, P3.5 bell/OSC-9 + NO_COLOR themes.
4. zh test matrix (R15) enters CI as a manual release-gate checklist until automatable.

### 5.5 Distribution

Zero new work: Makefile `go:` target already builds `bin/carina-tui`; `scripts/package-release.sh:153` already stages it into the release tarball; homebrew/npm launcher templates unchanged. Binary grows by the Charm deps (single static Go binary, no runtime deps). The `no-node-runtime` CI job passes by construction.

---

## Appendix: what would change this decision

- A mature, multi-maintainer React-model Rust framework with proven CJK input (none existed as of mid-2026 — the survey's explicit finding).
- Carina growing a Rust-side surface anyway (e.g., the CLI renderer migrating for other reasons) — re-run §3.4's everything-Rust option.
- Charm's v2 stalling (watch: release cadence, #874) — ratatui remains the standing runner-up with its spike results on file.
