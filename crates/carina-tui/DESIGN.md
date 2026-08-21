# Carina TUI Aesthetic Design System (DRAFT)

> Status: **draft law for aesthetic work**. Live SSOT:
> `docs/research/competitive-2026-08/18-tui-aesthetic-post-0.8.29.md`.
> `styles.md` is the renderer contract. When a P0 slice ships, rewrite the
> matching `styles.md` section. Do not keep two competing empty-state stories.
>
> Baseline: Carina v0.8.40. Chrome P0, conversation-document P0, recovered
> failures leaving the page, collapse-clear, honest Grok isolation copy,
> hot-path P0, Intent-Meta prompt, session-dialogue hydrate, unboxed chat
> tables, history-fork rewind, constitution A–D, Harness naming, todo/web.search,
> prefix-stable skills, Grok ACP stdio persist, and Grok cache isolation
> robustness **landed**.
> Steal principles, not pixels.
> Rejected wholesale: GrokNight / TokyoNight, OMP π branding, Claude terracotta
> as Carina identity, Jcode idle donut, Buddy/pets, Codex 36-frame ASCII loops.

This document exists because `styles.md` already proves Carina can be
**correct**. Correct and ugly is still **FAIL**. The first open must feel
designed. Long sessions must not fatigue. Status must be scannable. Tools must
be isolated from dialogue. Errors must not shout. `NO_COLOR` must still show
hierarchy.

---

## 0. Authority

| Rank | Document | Owns |
|------|----------|------|
| 1 | `docs/brand/AGENTS.md` | Mark, brand-rose vs ion-cyan, no fake wordmark |
| 2 | `docs/brand/design-system/design-tokens.json` | Canonical hex |
| 3 | This file | Terminal aesthetic: presence, chrome, motion, empty/error |
| 4 | `styles.md` | Renderer contract, 3-hue budget, ScreenMode, density numbers |
| 5 | `VOICE.md` | Copy formula |
| 6 | `theme.rs` / `glyphs.rs` | Sole palette and sole glyph registry |

Brand-rose (`#8e4053`) identifies the **mark**. It is never the interaction
color. Ion-cyan (`#8edbd2`) is focus, live work, and added-line emphasis.
Root canvas stays `Color::Reset`. Transcript still uses at most three saturated
foregrounds at once: ion-cyan, copper-amber, event-red.

**Token drift (must fix):** `theme.rs` `Palette::dark().brand` is currently
`#de859b` (`222,133,155`). Canonical is `#8e4053`. The TUI mark must match
the identity master, not a brighter improvisation.

---

## 1. Semantic color tokens (terminal)

Renderers consume helpers on `Theme`. Never `Color::Rgb` outside `theme.rs`.

| Token | Role | Where it may appear |
|-------|------|---------------------|
| `text` (`Reset`) | Body | Assistant answer, user body, overlay body |
| `gray_bright` | User identity | User `❯` |
| `gray` / `muted` | Secondary, settled receipts | Descriptions, completed tool titles |
| `gray_dim` | Tertiary / meta | Thinking, timestamps, paths, chrome rest |
| `brand` | Mark only | 10×5 braille glyph. Never composer, never buttons |
| `accent` | Interaction / live / add | Focus ring, focused composer border, live activity, `+` lines, links |
| `warning` | Attention still live | Approval wait, live tool, context heat, notices |
| `danger` | Failure / remove / destructive | Failure cell mark, `-` lines, destructive confirm. **Not** a full-frame fill |
| `success` | Non-transcript completion | Settings specimen, media ready, doctor. **Never** assistant identity |
| `user_message_bg` | User band | User turn only; Basic/`NO_COLOR` → Reset |
| `selection_bg` | One selected row | Lists, overlays. Never two adjacent filled bands |
| `border` | Resting chrome | Idle composer, header hairline, unfocused overlay |
| `code_*` / `diff_*` | Code and workbench diffs | Fenced code; `/changes` hunk pane. Transcript diffs stay fg-only |

Type ramp on one line: **text → muted → dim**. Keys are dim. Labels are muted.
Titles are accent+bold only while that object is the live focus.

Completed work recedes. Live work is the only saturated object in the
conversation column besides the focused composer.

---

## 2. Border and box law

Pick **one** chrome family and keep it:

| Mode | Outer chrome | Tables | Tree / quote |
|------|--------------|--------|--------------|
| Unicode / Nerd | Rounded `╭╮╰╯` + `─│` | Sharp `┌┬┐` only | `│ ` |
| ASCII | Plain `+` / `-` / `\|` | Same as chrome | `\| ` |

**Allowed boxes:** composer (the signature), overlays/modals, product menu,
slash/file/history popups, `/changes` workbench, plan review, approval.

**Forbidden boxes:** transcript turns, status/notice rows, header, idle empty
mark. Dialogue is a document, not a stack of cards.

**Hairline vs box:** a single `─` rule is a separator, not a field. Do not mix a
top-only hairline composer with fully rounded overlays and then call it a
system. The conversation composer is an **open-sided field** (top + bottom
rails, no left/right). Header may keep one bottom hairline. Overlays stay
four-sided rounded.

**Junctions:** rounded corners have no rounded tees. Nested rules inside a
rounded overlay use `─` fill, not a second inner rounded box.

**Question lists** use `glyphs.selected()` (`▌ ` / `> `), never a hardcoded
ASCII `>`.

---

## 3. First open / empty conversation

`styles.md` currently says the conversation is “not a repeated welcome
screen.” Keep that: **no looping poster after the first committed turn**.
Amend the empty state. A 24-row void with two instructional lines parked on
the composer is not an operating surface. It is an unfinished product.

Empty transcript (no blocks):

```
                    ⠀⢀⣀⣀⡀⣠⣶⣶⣄⠀          ← 10×5 braille, brand-rose
                    ⠀⣿⣿⣿⣿⣿⣿⣿⣿⠀
                    ⠀⠙⣿⣿⡏⠀⠈⢻⡇⠀
                    ⠀⢰⣿⣿⣿⣦⣴⣿⡿⠀
                    ⠀⠈⠻⠿⠟⠁⠀⠀⠀⠀

                         Carina
              Type a request, or /help · /model
```

Rules:

1. Center the mark + name + **one** hint in the reading column, not at the
   bottom margin.
2. Workspace name lives in the header, not “Ready in {dir}”.
3. Composer placeholder stays “Describe the change you want to make.”
4. Height < 22 or width < 48: drop the mark, keep `Carina` + hint.
5. ASCII mode: no braille (would tofu). Use the wordmark line only.
6. After the first user or assistant block, this composition is gone forever
   for that session. New conversation may show it once.
7. Motion: optional **one-shot** shine on process-first empty paint only
   (see §8). Default is static. `CARINA_REDUCED_MOTION` is always static.

Do not restage Grok’s hero menu or OMP’s dual-column “Welcome back!” poster
inside every empty chat.

---

## 4. Composer signature

The composer is the only persistent widget. It must be recognizable in one
glance, including `NO_COLOR`.

Three reference signatures — steal the job, not the skin:

| System | Signature | Do not copy |
|--------|-----------|-------------|
| Claude | Open-sided **top + bottom** rails; no left/right. Idle gray; bash gets a mode color. Dashed `╌` is for **diff/tool envelopes**, not the idle prompt | terracotta, `⏺`, spinner verbs |
| Codex | No drawn border. 2-col gutter `›` + a **soft fill band** from OSC-probed default bg | magenta identity, 36-frame ASCII loop |
| OMP | Four-sided `boxRound`; status painted into the **top rule** | π, powerline, `statusLineBg` fill |

Carina already paints overlays as four-sided rounded cards and the root as
`Color::Reset`. A second full-width filled OMP card around the prompt would
stack boxes on a transparent page and fight the Reset-root law.

**Chosen signature:** Claude’s open-sided rails **plus** OMP’s “status lives
in the top rule.” Add the missing **bottom** rail. Do not add left/right
walls. Do not fill the composer with `surface`.

Unicode idle:

```
────────────────────────────────────────────────────────────
 ❯ Describe the change you want to make.
────────────────────────────────────────────────────────────
```

Unicode focused: both rails `accent` + bold. Unicode running (status in the
**top** rule):

```
─ ⠋ Checking the patch boundary 0s · Esc pause ─────────────
 ❯
────────────────────────────────────────────────────────────
```

Unicode queued (second inner row only if queue is protected and non-zero):

```
─ ⠋ Checking the patch boundary 0s · Esc pause ─────────────
 queue 2
 ❯ follow-up
────────────────────────────────────────────────────────────
```

ASCII: `-` rails, `> ` prompt, same geometry.

Rules:

- Idle rails = `border`. Focused = `accent` + bold. Approval-waiting = `warning`.
  Failed run notice = `danger` on the **top rail only**, never a red fill.
- Prompt glyph remains width-locked `❯ ` / `> `.
- Status segments live in the top rail: activity, elapsed, interrupt hint.
  Queue depth is a protected segment. Critical context (`≥ 85%`) is a
  protected segment. Routine HITL / Isolation / ScreenMode stay in Settings.
- Idle top rail may be empty fill (`─`), not a telemetry dump.
- Empty draft = 1 inner row; grows upward to 5. Rails grow with the draft.
  Transcript shrinks; the field does not jump horizontally.
- Overlay composers (plan comment, questions) may keep four-sided rounded
  `Borders::ALL`. The conversation shell must not.
- Paste / file / image chips use the **live** `Theme`, never
  `Theme::detected(None)`. Chips are muted pills, not reversed cyan shouts.
- `NO_COLOR`: reverse/bold prompt + both rails still present as `─` / `-`.

This replaces the current “top hairline only + muted ❯ on Reset”. A four-sided
rounded composer remains allowed **inside overlays** (ask dialog, plan comment),
not on the conversation shell.

---

## 5. Status vs notice

Keep Jcode’s split. Do not collapse them.

| Row | Job | When height is 0 |
|-----|-----|------------------|
| Notice | Ephemeral operator fact (goal paused, copied, cache, steer) | Nothing to say |
| Status | Live run state (inside composer top rule after P0) | Idle |

Idle is allowed to be visually quiet. It is **not** allowed to be a hole.
When idle, the composer box itself is the scannable object. Do not invent a
debug footer of provider/model/reasoning/`ctx`/`ledger`.

Header (conversation, 1 content row + hairline):

- Left: product trigger `Carina ▾` (opens menu). Below 24 cells, keep the
  word; never squeeze the braille mark into this row.
- Right: **one** fact. Priority: Review/Resume (warning) → Mode → Model.
  Drop `reasoning off` from the always-visible row.
- Product name is `text`+bold at rest, `accent`+bold only while the menu is
  open. Mode may use accent. Do not paint both name and mode accent at once.

---

## 6. Transcript hierarchy

The transcript is a document. Assistant answers are the page. Tools are
receipts. Risk is never collapsed away.

| Cell | Mark | Color | Enclosure |
|------|------|-------|-----------|
| User | `❯ ` | `gray_bright` + band on occupied cells only | none |
| Assistant | `• ` | quiet mark; body `Reset` | none |
| Thinking live | `⠋ ` (spinner + space) | spinner `warning`+bold; label DIM/ITALIC | rail `┃` |
| Thinking settled | `▾ Thinking` | `gray_dim` + DIM/ITALIC | tree `│ ` |
| Tool live | `▸ {Intent}` | `warning` title | 1-col `┃` accent rail |
| Tool settled | `▸ {Intent}` | `gray` title, no bold | tree `│ ` or none |
| Tool group | `▸ Explored ×N · a, b` | settled | none |
| Approval | `! Action` | `warning` (not danger) until resolved | rail while waiting |
| Denied tool | settled `▸`/`Run` | settled title; muted `已拒绝` | none |
| Patch | `▸ Edit` + `+N`/`-N` | cyan / red counts | rail while applying |
| Failure | `✗ ` | `danger` mark, body Reset | rail |
| Compact/notice | `• ` | `gray_dim` | none |

Intent-primary grammar stays: `icon  Intent  ·  dim how`. Path/command never
outranks intent.

Live tool rail (`┃`, ASCII `|`) is the Grok principle adapted to Carina:
**one column of state**, not a four-sided tool card on every read. OMP-style
full boxes are reserved for expanded output overlays and `/changes`.

Assistant streaming is body growth plus the composer-top spinner. Do not
shimmer the whole answer. Live thinking uses the **same** braille activity
spinner as the composer top rail, width-locked to two cells (`⠋ ` matching
`▾ `), never in the answer body. Do not invent a second cycle (`· ✢ ✳ ✶`).

Density numbers in `styles.md` stay. Compact remaining the default is fine
**after** the composer and empty state exist. Density cannot rescue a void.

---

## 7. Empty / error / loading / success

| State | Look |
|-------|------|
| Empty chat | §3 identity, not “No data” |
| Empty list | one muted sentence inside the existing overlay |
| Loading | spinner only in the object that is waiting (composer top, thinking header, file viewer title). Glyph is 1 cell; thinking header pads it to 2 cells to match disclosure |
| Warning | amber mark + sentence. No amber fill |
| Failure cell | `✗` + outcome + escape hatch actions. Body stays Reset. Actions are `[ Retry ]`-style, not a flat sentence |
| Approval overlay | **warning** rounded border, not a full-frame danger box. Destructive confirm may use danger on the confirm button only |
| Success | recede. Do not flash green across the transcript |

Copy still follows `VOICE.md`: what happened, what we did, how to change it.
No protocol English (`ctx`, `ledger`, `soft-interrupt`, `n=`) on these rows.

---

## 8. Motion discipline

Motion proves liveness. It is not décor.

| Motion | Cadence | Starts | Stops |
|--------|---------|--------|-------|
| Activity spinner (composer top / thinking header) | 33 ms (~30 fps glyph, same as today) | execution running or thinking | completed, failed, paused, idle |
| Status spinner (provider validate etc.) | 80 ms | secondary work | that work ends |
| Empty-state shine (optional P1) | 12 fps, 4 s cycle, ~1.3 s sweep, then park | process-first empty paint | rest frame cached; never loops |
| Hover / focus | none | — | style change only, no deadline |
| Stream body | none | — | tokens append; no cosine wash |

Idle, completed, failed, paused, approval-waiting (except a slow pulse on the
waiting diamond if we add one), and unknown states request **no** animation
ticks.

`CARINA_REDUCED_MOTION` and unfocused terminal: freeze every decorative
glyph on its first frame. Semantic event redraws continue.

Never animate completed chrome. Never run a 30 fps wave across settled tools.
Never put an idle donut in leftover height.

---

## 9. ASCII / braille / spinner

| Class | Unicode / Nerd | ASCII | Notes |
|-------|----------------|-------|-------|
| Mark | canonical braille 10×5 | omit | never tofu |
| Prompt | `❯ ` | `> ` | width 2 |
| Spinner | `⠋⠙⠹⠸⠼⠴⠦⠧` | `\|/-\\` | 1 cell every frame |
| Thinking live | same spinner + space | same | header only, width 2 matching disclosure |
| Success / fail | `✓` `✗` | `+` `x` | with words |
| Disclosure | `▸` `▾` | `>` `v` | |
| Composer box | rounded | `+`/`-` | |
| Accent rail | `┃` | `\|` | live work and risk |

`auto` stays Unicode, never guesses Nerd Font. `NO_COLOR` does not force
ASCII.

---

## 10. Information hierarchy (scan test)

A trained operator, 3 seconds, 80×24, **two fixtures**:

**Fixture A — four short user turns + one denied Run + one assistant answer.**
This is the daily product. They must name without hunting:

1. What they asked (user **pills**, not full-width slabs).
2. What the answer is (unboxed body, louder than tools).
3. That the Run was refused (muted settled receipt, not a crash rail).
4. That leftover height is rest, not a missing widget.

**Fixture B — 8 tools + 1 patch + 1 failure.** They must name:

1. What is live (composer top rule / thinking spinner / tool `┃`).
2. Whether anything is waiting or broken (warning/danger marks).

If they have to read provider names, reasoning levels, or `ctx 12k/200k` to
know those facts, the layout has failed. If Fixture A still looks like a
syslog with a signed composer, **chrome P0 was not the product**.

---

## 11. Compatibility

| Environment | Visual |
|-------------|--------|
| Truecolor | mineral tokens |
| ANSI-256 | existing Lab quantize |
| Basic | Reset fills, terminal accents, DIM secondary |
| `NO_COLOR` | reverse/bold/underline structure, same geometry |
| tmux / SSH | no assumed COLORTERM; honest ColorLevel |
| Windows ConHost | ASCII glyphs; no braille mark |
| Minimal ScreenMode | native scrollback; same open-sided composer rails; no mouse hover styles |
| Width < 48 | drop mark, keep composer rails, one header fact |

---

## 12. What this draft does not change

Capability kernel, hash-chained audit, transactional patch, fail-closed
approvals, ScreenMode as projection, density as presentation over one
semantic tree, live = replay, `theme.rs` as sole palette, three-hue
transcript budget, brand-rose as mark-not-interaction.

---

## 13. Acceptance (aesthetic)

- First paint of an empty conversation produces a designed identity, not a
  hole.
- The composer is a signed field at rest and in focus, including `NO_COLOR`.
- Status is scannable from the composer top rule; notices never steal it.
- A 4-turn short chat is a document: user pills, unboxed answer, leftover
  rest. It must not look like a syslog with a pretty footer.
- Settled tools are quieter than live tools and quieter than the answer.
- Policy-denied tools recede. Only `FailurePresentation` uses danger rail.
- Live thinking uses the activity spinner at two cells; settled thinking
  restores disclosure without shifting the title.
- `/theme` and Settings preview `auto|dark|light` live without a catalog.
- Approval does not paint a red room. Failure is a mark plus a sentence.
- Reduced motion and `NO_COLOR` keep layout and rank.
- Golden frames at 80 / 120 / 160 × EN / zh-Hans record empty, idle
  composer, running composer, live-tool rail, **and** a short-chat fixture.

Functional and still ugly remains **FAIL**.

---

## 14. Open P0 (conversation document)

Shipped on 0.8.29 and **kept**: empty identity, open-sided composer, quiet
header, live `┃`, approval warning, thinking spinner, `/theme`.

Shipped on 0.8.30:

| ID | Knife | Status |
|----|-------|--------|
| A010 | User band on occupied cells only | shipped 0.8.30 |
| A012 | Policy-denied tools recede | shipped 0.8.30 |
| A011 | Assistant answer louder than settled tools | shipped 0.8.30 |
| A013 | Leftover height is rest, never a donut | shipped 0.8.30 |

Shipped on 0.8.31: recovered/settled-retry failures leave the reading column;
collapsing a tool clears wrap remnants from the unread gutter.

Do not start A102 shine or a theme catalog until Fixture A is the daily binary.
