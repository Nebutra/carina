# Carina TUI Aesthetic Audit

> Date: 2026-08-20  
> Carina: **v0.8.28** (`35322f4`)  
> Role: Terminal Aesthetic Architect — aesthetic is a first-class product force.  
> **Verdict: FAIL.** The TUI is operationally mature and visually unfinished.  
> “It works / cells are typed / goldens exist” is not a pass.

Competitor trees (local, no README-only claims):

| System | Path | What was read |
|--------|------|----------------|
| Grok Build | `…/competitors/grok-build` (`xai-grok-pager`, `xai-grok-pager-render`) | Theme catalog, welcome braille+shine, status/turn-status, accent rails, event-driven ticks |
| Oh My Pi | `…/competitors/oh-my-pi` | `theme-schema.json`, `boxRound`, editor top-border status, welcome intro, tool frames |
| Codex CLI | `…/competitors/codex/codex-rs/tui` | `styles.md`, OSC 10+11 pair (100ms), cosine shimmer on default fg/bg, 2-col `›`/`•`/`└` tree, fill-band composer (no drawn border), 36-frame ASCII welcome |
| Claude Code | notes v2.1.88 + leaked `src/` matching installed **2.1.220** | terracotta `#d77757`; PromptInput **open-sided** round rails (not a 4-sided dashed box); dashed `╌` = DiffFrame only; spinner `· ✢ ✳ ✶ ✻ ✽`; StatusLine opt-in |
| Jcode | `…/competitors/jcode` | notification vs status split, intent-primary tool row, leftover-height donut (rejected) |

Carina sources: `crates/carina-tui/{styles.md,theme.rs,glyphs.rs,conversation.rs,product_header.rs,terminal_logo.rs,app/render.rs,semantic_cell.rs,tool_projection.rs}` plus production goldens under `src/app/snapshots/` and toy goldens under `tests/snapshots/`.

Related but **not** this verdict: `10-visual-density-program.md` closed density/grammar ISSUEs. That program made the log *typed*. It did not make the product *beautiful*. Honesty refresh `16-post-0.8.27-refresh.md` said “no new P0/P1” for **truth**. This audit opens an aesthetic program.

Design draft (actionable): [`crates/carina-tui/DESIGN.md`](../../../crates/carina-tui/DESIGN.md).

---

## Phase 0 — Ruler: what “good” actually looks like

Extracted as **roles**, not hex to copy.

### Color semantics

| System | System |
|--------|--------|
| **Grok** | One `Theme` struct. Gray ladder `gray_dim → gray → gray_bright`. Accents are **speaker/state** (`accent_user/assistant/tool/error/success/running`). `/theme` live-previews without persist. Minimal locks `Color::Reset` + ANSI-16. “All colors come from Theme.” |
| **OMP** | JSON schema: `vars` + required `colors`. Empty string = terminal default fg/bg. Tool frames have pending/success/error **backgrounds**. Status line has **13 segment tokens**. Completed borders recede to `dim`. |
| **Codex** | `styles.md`: default fg, cyan tips/selection, green add, red fail, magenta identity. **Avoid custom RGB** except shimmer (blend of **queried** default fg/bg). OSC **10+11 pair**, 100ms, both required or conservative dark. |
| **Claude** | One warm brand hue `claude` `#d77757` (`rgb(215,119,87)`). Twin `claudeShimmer` only on the live spinner verb. OSC 11 auto. High-sat tokens (`bashBorder`, `permission`) are **mode-only**, never default chrome. |
| **Jcode** | User / AI / tool / dim / accent / queued roles. `Color::Reset` sacred. Harmony scored in Oklab. |
| **Carina already has** | `theme.rs` semantic fields, polarity, Lab quantize, 3-hue transcript budget, Reset root. **Missing:** named catalog + live preview; conversation never uses `brand`; dark `brand` hex **drifts** from `#8e4053` to `#de859b`. |

Ruler: semantic tokens, a three-step type ramp, completed work goes gray, one accent dominates a component.

### Borders and boxes

| System | Law |
|--------|-----|
| **Grok** | Rounded overlays. Conversation is **not** boxed. Speaker/state = 1-col `┃` rail. Prompt = left accent rail, not a 4-sided card. Minimal hides rails. |
| **OMP** | `boxRound` `╭╮╰╯` everywhere except tables (`┌┐`). Composer is a **full rounded box**; status is painted into the **top rule**. Tool results are framed; success frame goes dim. |
| **Codex** | No full-screen `Block`. Chrome is a **2-col gutter** (`›` user, `•` agent/tools, `└`/`│` children). Rare **dim, content-sized** rounded cards (session `/status`). Composer = tinted fill, **no drawn border**. |
| **Claude** | Idle prompt is **open-sided** (top+bottom rails, no left/right). Custom dashed `╌`/`╎`/space corners is the **diff/tool envelope**, quiet `subtle` color; high-sat lives *inside* the envelope. Do not put a heavy 4-sided card around the prompt. |
| **Jcode** | Chat shell **unboxed**. Rounded boxes only for exceptional cards (plan, reconnect, overlays). Composer is `N>` with no `Block`. |

Ruler: pick one chrome family. The idle prompt must be a **signed field** (open-sided rails, fill band, left rail, or a box) — never a lone top hairline. Do not box every turn. Carina conversation composer chooses **open-sided rails**, not a 4-sided card.

### Status vs notice

| System | Law |
|--------|-----|
| **Grok** | Top gray session bar + **turn-status row height 0 when idle** + 2s toast. Chips joined with ` │ `. |
| **OMP** | Status **is** the composer top border. Error banner is a **separate** pinned block above the editor. |
| **Codex** | Configurable status line; composer footer; default-fg hierarchy. |
| **Claude** | Spinner row sits **between** transcript and input and is **not** history. `StatusLine` is **opt-in** (`statusLineShouldDisplay`). Footer pills hide when a custom statusline exists. Short terminals drop StatusLine first. |
| **Jcode** | **Hard split:** status always 1 row of live run state; notification 0\|1 ephemeral. Notices must not occupy the activity row. Idle status may be blank **because** the composer caret is the object. |

Ruler: live state ≠ notices. Idle may be quiet only if the composer is still a visual anchor.

### Empty / error / loading / welcome

| System | Law |
|--------|-----|
| **Grok** | Fullscreen: braille logo + 12 fps diagonal shine (4 s cycle, ~1.3 s sweep, then rest). Hero rounded box ≥90 cols. Height <22: no logo. ConHost: hide braille. Minimal: **one-shot** committed card, no loop. |
| **OMP** | Dual-column welcome poster, **3 s one-shot** intro at 30 fps, then **cache**. Empty lists = dim sentence, not a box. |
| **Codex** | Onboarding ASCII **36-frame variants** at 80 ms, gated by min 60×37, suppressible, `.` rotates variant. Shimmer on loading copy. |
| **Claude** | Empty REPL is still a product because the **open-sided prompt rails** are there. No looping poster. |
| **Jcode** | Vertically centered header + numbered starters; leftover height may donut (Carina **rejects** the donut). |

Ruler: first open is a **poster that sits still**. Errors are compact evidence. Spinners are 1 cell and stop.

### Diff / stream / tools

| System | Law |
|--------|-----|
| **Grok** | `◆ Verb path (meta)`. Read/search: no rail. Execute: rail + in-viewport wave. Diff: gutter unpainted, bg on content. Collapse groups. |
| **OMP** | Framed tool with `╭─── ✔ write … · 12ms ─╮`. Collapse budgets (3 lines, 8 items). Consecutive reads group. Streaming tail window so the live edge stays at the bottom. Diff gutter **3 digits** through 999 (no jump). |
| **Codex** | `• Ran cmd` then `│` wrap / `└` dim output, **5 lines** (head+tail). In-flight: shimmer `•` + bold verb. Done: green/red `•`. Diff header `• Edited path (+N -M)` with content-only tints from OSC bg. |
| **Claude** | Tool row: `⏺ Name` + hanging `  ⎿  ` result. Heavy diff in **dashed + subtle** open frame; saturation only **inside**. Thinking spinner `· ✢ ✳ ✶ ✻ ✽` ping-pong 120ms in the **spinner row only**. Tokens appear after **30s**. Thinking blocks hidden unless verbose/`Ctrl+O`. |
| **Jcode** | One line: `✓ bash · intent · dim how`. Intent never adds a second row. |

### Animation restraint

| System | Cadence | Stop condition |
|--------|---------|----------------|
| Grok | Idle = **zero ticks**. Slow 12 fps welcome. Spinner ~7.5 fps. Wave only in-view. | `needs_animation()` false |
| OMP | Status 12.5 fps. Welcome 3 s then cache. Thinking ~6.7 fps breath. | completed chrome is static gray |
| Codex | Shimmer blends default fg/bg; reduced motion = plain text. ASCII welcome 80 ms. | `MotionMode::Reduced` |
| Jcode | Spinner 12.5 fps **patched in one cell**. Idle donut = leftover only. | processing ends |

Ruler: motion is demand-gated. Completed work does not sparkle.

### Hierarchy / negative space

Good products spend leftover cells as **blank**, not as extra chrome. Assistant answers are unboxed. Tools recede when done. Status uses compact numbers and **fixed-width** hover so bars do not dance. Outer pad exists. Welcome vertically centers.

---

### Phase 0 conclusion

The ruler is not “more color” or “more Unicode.” It is:

1. A **signed input**.
2. A **first-open identity** that then sits still.
3. **Live vs settled** as a visual rank, not only a reducer flag.
4. Status you can scan; notices that vanish.
5. Motion only while something is unfinished.
6. One border family.

Carina already owns (1) tokens, (2) glyph tiers, (3) density numbers, (4) notice/status split in code, (5) a 3-hue budget. It spends none of that on **presence**. The contract is a style guide for an operations ledger.

---

## Phase 1 — Carina visual inventory

Evidence is production goldens + source, not “the feature exists.”

### 1. Startup / Welcome

**Looks like:** Conversation has **no welcome**. Comment in `app/render.rs:2718`: “operating surface, not a repeated welcome screen.” Empty chat golden `golden_frames__golden_empty_conversation.snap` (80×24): ~20 blank rows, then `Ready in workspace` (Reset+BOLD) and `Type a request, or /help · /model · /status` (muted), parked on the bottom margin. Composer placeholder is a third instructional line.

The 10×5 braille mark (`docs/brand/assets/logo/carina-symbol-terminal-braille.txt`, `terminal_logo.rs`) is **only** on expanded setup `ProductHeader` (≥ wide/tall). Conversation header is wordmark-only by law (`product_header.rs`). Shine is a **static** one-cell diagonal of BOLD/muted, not Grok’s timed sweep.

Setup scenes use the 6-row expanded header (mark + Provider/Model/Workspace). Diagnostics is a centered 100×30 “Carina · Diagnostics” panel.

**Ugly:** 8 missing empty, 11 missing/hidden mark, 12 no hierarchy on first paint.

**Ruler:** Grok/OMP/Codex all spend first open on identity. Even Grok Minimal commits a one-shot card into scrollback.

### 2. Main transcript

**Looks like:** `semantic_cell_gallery_120.snap` / `visual_density_en_120.snap`:

```
 ❯ user request                    ← gray_bright BOLD on #262b2c band
 • assistant body                  ← gray_dim / Reset
 ▾ Thinking  …                     ← DIM|ITALIC, │ rail
 • Read    intent  path            ← settled gray_bright
 ▸ Explored ×5 · a, b, …
 ! Action  … needs approval
 ▸ Edit    …  +1  applied
 ✗ Execution failed  ·  model  ·  2 attempts
 • Diagnostic  Context compacted
```

Reading column 120. Compact gaps 1 / related 0. Comfortable inserts blank lines (`visual_density_comfortable_en_120.snap`) — still a rail-prefixed log. User band is a muddy 1-row rectangle. Assistant identity is a period. Outer selection gutter is blank when idle.

**Ugly:** 1 wall, 6 no breath (compact), 12 equal weight.

**Ruler:** Grok unboxes answers and rails tools; OMP frames tools and recedes success; Jcode one-line intent.

### 3. Prompt / composer

**Looks like:** `render_composer`: **TOP hairline only**. Focus = ion-cyan BOLD `─`; idle = `#344144`. Prompt `❯ ` 2 cells. Empty = 1 row, max 5. Placeholder muted. Production `product_menu_en_unicode.snap`:

```
 ──────────────────────────────────────────────────────────────────────────────
 ❯ Describe the change you want to make.
```

Idle unfocused prompt is muted gray on Reset — unsigned. Paste/file/image chips use `theme.action()` = cyan BOLD+REVERSED; `clipboard_image.rs` / chips call `Theme::detected(None)` and can desync from live polarity.

**Ugly:** 10 unsigned prompt, 4 hairline vs rounded overlays, 9 chip shout.

**Ruler:** Claude open-sided top+bottom rails; OMP status-in-top-rule; Codex fill band + `›`; Grok left `┃`. Carina has a **top hairline only** — the missing bottom rail is the whole gap.

### 4. Status / notification

**Looks like:** `ComposerChrome` two rows **above** the hairline. Idle golden `composer_chrome_en_idle_80.snap` / `status_idle.snap`: **blank 80×2**. Running: `⠋ Running focused checks 0s  Esc pause safely` in ion-cyan. Queued: second row `queue 2`. Approval: `waiting for approval` amber. Priority notice replaces narrative (danger). Overlay covering a notice does not count as seen.

Header still shows `model · reasoning off · Build` on every conversation frame (`product_menu_en_unicode.snap`).

**Ugly:** 5 chaotic (idle hole + running ticker + header dump), 8 idle emptiness.

**Ruler:** Jcode split is already *in the reducer*. OMP puts status in the composer rule so idle still has an object.

### 5. Tool results

**Looks like:** Compact collapsed = header only (0 output lines, 0 extra members). Comfortable = 2 members / 3 lines. Group title `Explored ×5 · paths`. Expanded budget 3–24 rows with “N lines omitted · key”. Overlay = rounded cyan “Tool output”.

**Ugly:** 1 wall when expanded; compact hides evidence behind an unnamed habit (`Ctrl-O` is in VOICE but the row does not look like a card you can open). No live `┃` vs settled dim.

**Ruler:** OMP dim success frames; Grok diamond+verb; Jcode intent line.

### 6. Diff / patch review

**Looks like:** Transcript `+` = ion-cyan fg, `-` = event-red fg, no diff **backgrounds**. `/changes` `fullscreen_patch_review_en_120.snap`: giant rounded cyan “Changes workbench”, three cramped columns, paths ellipsized to `…y-0184` / `…time/patch.rs`, then **many empty rows** inside the box. Approval overlay body is a **raw unstyled string**. `golden_diff.snap` paints **success green** `+` — not production (cyan).

**Ugly:** 13 alignment, 6 wasted interior, 4/12 color disagreement transcript vs workbench vs toy golden.

**Ruler:** Grok content-only diff bg, stable gutters; OMP 3-digit gutter.

### 7. Error / warning / success

**Looks like:** Failure cell danger BOLD `✗` + guttered reason + `Retry current  Replay original  Details  Copy ID` as a sentence. Approval overlay **danger rounded border** 78×26. Success `✓` is spectral green in settings specimen / media — banned from transcript identity (correct) and therefore **almost invisible** as delight.

**Ugly:** 9 screaming approval frame; success has no quiet pleasure.

**Ruler:** Claude/Codex fail as a mark + sentence; OMP error banner max 3 lines, dismissed on next send.

### 8. Empty states (other)

Muted one-liners in lists (`No matching prompts`, scan/fail). File popup still draws a 4-row framed box when empty. Context overlay speaks `ctx` / `ledger` / `hash`.

**Ugly:** 8, plus VOICE slang.

### 9. Slash / file / history dropdowns

Rounded **accent** boxes, bottom of transcript, max 8 rows. Slash names 14-cell accent field; selected row also `selection_bg` → double cyan. Three near-identical popups.

**Ugly:** 12, 4 box-on-box.

**Ruler:** Jcode overlays that do not move status/transcript; OMP autocomplete under cursor.

### 10. Modals / settings / theme

Same cyan rounded card everywhere (Settings 62×20, Help, Status, Symbols, Plan, Queue…). Symbols preview is a **padded empty box** with a specimen that sits green+amber+red+cyan together (budget violation as a sample). **No `/theme` and no Settings row for polarity.** `CARINA_THEME=dark|light|auto` + OSC 11 only. Doctor title hardcoded `" Doctor "`. Question options hardcode `>`.

**Ugly:** 3 weak theme UX, 6 empty modal guts, 4 mixed `>`.

### 11. Streaming

No caret, no shimmer, no per-token style. Commentary = gray_dim (looks stuck). FinalAnswer mark cyan, body grows. Only live motion is 33 ms braille in chrome (`frame_scheduler.rs` ACTIVITY 33 ms, STATUS 80 ms). Sync output exists to hide tear. Reduced motion freezes glyphs.

**Ugly:** 14 — either a spinning footer or a dead paragraph. No in-object liveness.

### 12. Header / product menu

```
Carina ▾   ·  product-menu conversation         model  ·  reasoning off   Build
────────────────────────────────────────────────────────────────────────────────
╭ Carina menu ─────────────╮
│▌ New conversation        │
```

Name and `Build` both fight for ion-cyan when the menu is open / mode is active. Braille mark forbidden on this row.

**Ugly:** 5, 11, 12.

### 13. Approval / plan

Plan review (`plan_review_en_80.snap`) is the **least ugly** overlay: rounded box, inner rule, selection `▌`, actions. Footer is still a keymap novel. Approval diffs unstyled. Question `>` alphabet break.

### 14. ScreenMode

Fullscreen vs Minimal vs Inline: same pixels minus mouse/header. Minimal is not a designed native-scrollback *look* (Grok Minimal commits a card and hides rails). Patch review becomes a compact overlay — second language for the same object.

---

### Phase 1 conclusion

Every daily surface except “typed cell kinds exist” fails the ruler. The strongest engineering (theme module, glyphs, goldens, 3-hue test) is invisible on first open because the conversation **refuses identity** and the composer **refuses to be a field**.

---

## Phase 2 — Ugly Patterns (one hit = FAIL)

| # | Pattern | Carina | Evidence |
|---|---------|--------|----------|
| 1 | Wall of text | **FAIL** | Transcript rails; expanded tools 24 lines; Status/Context/Doctor dumps; plan footer keymap |
| 2 | Hardcoded colors | **PARTIAL** | Renderers clean (`style_discipline.rs`). `theme.rs` is the owner. Chips `Theme::detected(None)` desync. Doctor/Question raw strings. `brand` hex ≠ tokens.json |
| 3 | No / weak theme | **FAIL** | Polarities exist; no catalog; no `/theme` preview; conversation unused `brand`/`surface`/`link` |
| 4 | Mixed borders | **FAIL** | Rounded overlays, hairline composer, sharp tables, Question `>`, toy goldens `┌┐` |
| 5 | Chaotic status | **FAIL** | Idle blank + header telemetry + cyan spinner sentence + notices |
| 6 | No breathing | **FAIL** | Compact related gap 0; empty copy sits on composer; workbench empty guts vs dense columns |
| 7 | Flicker / jitter | **PARTIAL** | Scheduler + sync output help; 33 ms spinner vs static UI; overlay `Clear` |
| 8 | Empty state missing/ugly | **FAIL** | 80×24 hole + two hints |
| 9 | Screaming errors | **FAIL** | Approval danger frame; reversed cyan chips; `✗` BOLD is OK, the red room is not |
| 10 | Unsigned prompt | **FAIL** | Hairline + muted `❯` |
| 11 | ASCII art crude/missing | **FAIL** | Canonical braille hidden on the surface users live in |
| 12 | No hierarchy | **FAIL** | User band, assistant bullet, tool header similar weight; markdown headings shout |
| 13 | Broken alignment | **FAIL** | `/changes` ellipsis noise; chip vs wrap; imported label vs 2-cell hang |
| 14 | Animation undisciplined | **FAIL** | No first-open motion; no stream pulse; chrome spinner always-on while running; no one-shot then stop for brand |

**Score: 12 FAIL, 2 PARTIAL, 0 PASS.**

---

## Phase 3 — Scores (reference average 8+)

| Surface | Reference best practice (source) | Carina current | Score | Main Ugly Pattern | Priority |
|---------|----------------------------------|----------------|------|-------------------|----------|
| First open / empty chat | Grok hero or Minimal one-shot card; OMP cached poster; Jcode centered identity | 20 blank rows + 2 hints on the composer | **2** | 8, 11 | **P0** |
| Composer | Claude open-sided rails + OMP status in top rule; Codex fill+`›` | Top hairline, muted `❯` | **3** | 10, 4 | **P0** |
| Streaming | Codex shimmer on *labels*; Grok in-view wave; Claude thinking spinner | Gray paragraph + footer spinner | **3** | 14 | **P0** |
| Header | One identity + one fact | `Carina ▾ · title` + model + reasoning + Build | **4** | 5, 12 | **P0** |
| Error / approval | Mark + sentence; warning border | Danger full overlay; sentence actions | **4** | 9 | **P0** |
| Transcript | Unboxed answers, receding tools, intent rows | Typed ledger, equal weight | **5** | 1, 12 | **P0** |
| Tool blocks | OMP dim frames / Grok diamond / Jcode one line | Headers + optional wall | **5** | 1 | **P0** |
| Status / notice | Jcode split; OMP top rule; Grok height-0 idle | Split in code; idle hole; running ticker | **5** | 5 | **P0** |
| Slash / mentions | Overlay, no layout jump | Cyan boxes, double accent | **5** | 12 | **P1** |
| Diff / `/changes` | Stable gutters, content bg | Cramped 3-pane, color mismatch | **6** | 13 | **P1** |
| Plan review | Grok a/s/c/q density | Decent box, keymap footer | **6** | 1 | **P1** |
| Settings / theme | Grok `/theme` preview; OMP JSON catalog | No picker; empty Symbols guts | **4** | 3 | **P1** |
| Glyphs / ASCII | OMP unicode/nerd/ascii scene | Registry exists; mark not used; Question `>` | **6** | 4, 11 | **P1** |
| Motion system | Demand-gated ticks | Scheduler exists; no brand motion; 33 ms spinner | **5** | 14 | **P1** |
| Minimal / tmux / NO_COLOR | Grok Minimal Reset lock | Geometry mostly OK; no designed Minimal look | **6** | — | **P2** |
| **Product first impression** | 8+ | Hole + unsigned prompt + cyan dump | **3** | 8, 10 | **P0** |

**Weighted product score: ~4 / 10. FAIL.**

Gap in one sentence: Carina designed a **theme compiler** and then rendered a **syslog**.

---

## Phase 4 — Deliverables

### 4.1 Design system draft

Canonical file: [`crates/carina-tui/DESIGN.md`](../../../crates/carina-tui/DESIGN.md).

It specifies: tokens (including brand-rose drift fix), rounded overlays, empty-state identity, conversation composer as **open-sided top+bottom rails** with status in the top rail (not a 4-sided OMP card), notice/status split, live `┃` vs settled tools, error restraint, motion table, `NO_COLOR`/ASCII/Minimal, and an explicit non-goals list (no GrokNight, no π, no terracotta identity, no donut, no Buddy).

### 4.2 P0 visual knives

Ship these until first open is not ugly. Do **not** wait for a theme catalog.

#### P0-A001 — Empty conversation identity

- **Problem:** 80×24 is a hole. `Ready in {workspace}` reads like a debug probe.
- **Reference:** Grok Minimal one-shot card; OMP centered logo then still; Jcode centered header.
- **Change:** `conversation.rs` `EmptyConversation`; `app/render.rs` `render_transcript` empty branch; `terminal_logo.rs`; i18n drop/replace `ReadyInWorkspace` on this surface; goldens `golden_empty_conversation` + product-menu empty snaps.
- **Accept:** 80×24 and 120×40 show centered 10×5 mark (unicode) + `Carina` + one hint. ASCII: word + hint only. After first block, gone. `make brand-check` still green. Mark uses canonical `#8e4053`.

#### P0-A002 — Composer is a signed field; status lives in the top rail

- **Problem:** Top hairline + muted `❯` is not a product. Idle chrome is blank; running chrome is a cyan sentence *above* a hairline.
- **Reference:** Claude PromptInput `borderLeft/Right=false` (open-sided rails); OMP status in the top rule; Codex fill-band + 2-col gutter (do **not** copy the missing-bottom-rail look we already have). Dashed Claude frames are for diffs, not the idle prompt.
- **Change:** `app/render.rs` `render_composer` + `render_composer_chrome_projection`; `layout_contract.rs` composer geometry; `conversation.rs` `ComposerChrome`; idle/running/queued/approval goldens. Add the **bottom** rail. Keep sides open. Do not fill `surface`. Do not wrap the conversation composer in `Borders::ALL`.
- **Accept:** Idle = top + bottom `─` (ASCII `-`) around the prompt. Focused rails = accent. Running: spinner + activity + Esc in the **top rail**, not a separate cyan dump. `NO_COLOR`: reverse/bold prompt + both rails still visible. Field grows 1→5 with draft; transcript shrinks. Overlays may stay four-sided rounded.

#### P0-A003 — Conversation header drops telemetry

- **Problem:** `model · reasoning off · Build` is always-on debug.
- **Reference:** Grok top bar is gray location; Jcode one model line; DESIGN.md “one right-hand fact.”
- **Change:** `product_header.rs` `ConversationHeader`; `render_conversation_header`.
- **Accept:** Left `Carina ▾`. Right only Review/Resume **or** Mode **or** Model (priority in styles). No `reasoning off`. Menu-open is the only time the name is accent.

#### P0-A004 — Live tool accent rail; settled tools recede

- **Problem:** Thinking, settled read, live edit, and failure are the same `│` weight.
- **Reference:** Grok `┃` for live execute; OMP success border → dim; Jcode `✓ name · intent`.
- **Change:** `glyphs.rs` accent rail set; `app/render.rs` transcript outer gutter / tool headers; `semantic_cell.rs` if a rail flag is needed (presentation only).
- **Accept:** Live tool/thinking-running: `┃` (ASCII `|`) in warning/accent. Settled: dim `│` or none. Assistant still unboxed. Compact still header-only. 3-hue test still green.

#### P0-A005 — Brand-rose token match + chips use live Theme

- **Problem:** Dark brand `#de859b` ≠ `#8e4053`. Chips can disagree with polarity.
- **Reference:** `docs/brand/AGENTS.md`; OMP “no one-off hex.”
- **Change:** `theme.rs` `Palette::dark().brand`; chip helpers to take `&Theme` / `self.theme`.
- **Accept:** `make brand-check`; no `Theme::detected(None)` in chip paint; mark on empty state matches identity.

#### P0-A006 — Approval / failure restraint

- **Problem:** Approval is a red room. Failure actions are a flat sentence.
- **Reference:** Claude/Codex mark+copy; OMP 3-line error banner; DESIGN.md warning border.
- **Change:** approval overlay `border_style` warning not danger; failure action row as `[ Retry ]`-shaped actions; keep `✗` + danger **mark**.
- **Accept:** Approval readable without a red flood. `NO_COLOR` still shows blocking via reverse title. Failure still cannot collapse-hide.

#### P0-A007 — Streaming liveness in-object

- **Problem:** Streaming looks like a stuck gray paragraph.
- **Reference:** Claude thinking glyphs; Codex shimmer on the **status label** only; Grok spinner on turn-status.
- **Change:** thinking header uses width-locked spinner while running; composer top rule already shows activity after A002. **No** body shimmer.
- **Accept:** While thinking/running, one 1-cell spinner in the thinking header **or** composer top rule (not both competing). Reduced motion: static first frame.
- **Landed (uncommitted on 0.8.28 tree):** live thinking replaces disclosure with the same activity spinner, padded to two cells (`⠋ ` / `| `) so settling back to `▾ `/`▸ ` does not shift the title. Composer-top and thinking-header share one elapsed clock and one glyph class; they do not run two cycles. Reduced motion freezes the first frame. Body is not shimmered.

### 4.3 P1 / P2

**P1**

| ID | Item |
|----|------|
| A101 | `/theme` + Settings: live preview `auto|dark|light` (Grok preview_arg pattern). **No** 80-theme catalog. **Landed (uncommitted):** `/theme` and Settings Theme row share one reversible preview; `tui_theme` persists; `CARINA_THEME` remains an disclosed override; `/theme dark` previews without saving. |
| A102 | Optional one-shot empty-state shine (Grok 12 fps, 4 s cycle, park). Process-first paint only. |
| A103 | `/changes` column measure: no 4-char ellipsis IDs; less empty guts; transcript `+`/`-` vs workbench `diff_*` documented as two palettes or unified. |
| A104 | Plan footer: one keycap row, not a help novel. |
| A105 | Question list uses `glyphs.selected()`. Doctor title through i18n. |
| A106 | Slash popup: selected row **or** accent name, not both. |
| A107 | Paste chips as muted pills, not reversed shouts. |
| A108 | Context/Status overlays: VOICE, no `ledger`/`hash`/`ctx` slang. |
| A109 | Toy `golden_frames` sharp boxes / green diffs aligned with production or deleted as non-product. |

**P2**

| ID | Item |
|----|------|
| A201 | Extra palettes **derived from** spectral mineral (not TokyoNight). |
| A202 | Minimal ScreenMode: designed native-scrollback card (Grok Minimal principle, Carina mark). |
| A203 | tmux/SSH/Windows ConHost fixture goldens for empty+composer. |
| A204 | Thinking glyph set `· ✢ ✳ ✶` as optional unicode extra, ASCII `...`. |
| A205 | Hover underline discipline already present; keep it static. |

### 4.4 Anti-patterns — never again

1. **Do not** treat goldens / “typed cells” / “feature wired” as an aesthetic pass.
2. **Do not** copy GrokNight, TokyoNight, OMP π, Claude terracotta, or Jcode donut/Buddy into Carina.
3. **Do not** paint `brand-rose` on buttons, composer borders, or assistant text.
4. **Do not** invent a second token set. Fix `theme.rs` to match `design-tokens.json`.
5. **Do not** mix dashed Claude boxes with rounded OMP boxes. Carina chrome is **rounded** (ASCII plain).
6. **Do not** box every tool. Rail live work; frame overlays and the composer only.
7. **Do not** loop welcome animation on every empty chat or every new turn.
8. **Do not** put provider/reasoning/ctx/ledger on the idle conversation header.
9. **Do not** use full-frame danger fills. Danger is a mark.
10. **Do not** shimmer assistant bodies. Shimmer/shine is brand or status labels.
11. **Do not** call `Theme::detected(None)` from a widget that already has `self.theme`.
12. **Do not** hardcode `>`, `" Doctor "`, or English protocol tails in product chrome.
13. **Do not** reopen kernel / audit / transactional-patch / yolo / MiniLM / ACP-as-UI to “make it pretty.”
14. **Do not** hide the braille mark in setup forever while the conversation is a void.

---

## Relationship to existing law

| Keep | Amend |
|------|-------|
| Reset root, 3-hue transcript, `theme.rs` owner, glyph tiers, density table, notice vs chrome split, live=replay, fail/diff/approval never collapse-hidden, brand-rose = mark | Empty transcript copy (`styles.md` “not a welcome” → “identity once, then operating document”); composer “top hairline” → **open-sided top+bottom rails** with status in the top rail; conversation header “mode + model + reasoning” → one fact; `Palette::dark().brand` hex |

Implementation starts only when the user says **刀** on a named P0 slice. This document does not ship pixels.
