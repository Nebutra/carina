# Carina TUI Aesthetic Audit — post 0.8.29

> Date: 2026-08-20
> Carina: **v0.8.29** (`36e2673`) was the chrome line. Conversation-document
> knives A010–A013 ship in **v0.8.30**.
> Role: Terminal Aesthetic Architect. Aesthetic is first-class. “It works”
> is not a pass.
> **Verdict: still FAIL.** Chrome was designed. The daily conversation was not.
> The previous P0 list in `17-tui-aesthetic-audit.md` is the cause.

This file **supersedes 17** as the live aesthetic SSOT. 17 remains the
competitor DNA card. Do not treat 17’s P0-A001…A007 as “the product is now
pretty.”

Competitor trees re-opened (source, not README):

| System | Path | Re-read for this refresh |
|--------|------|--------------------------|
| Grok Build | `…/grok-build` `xai-grok-pager` + `xai-grok-pager-render` | Theme `accent_user` drives prompt `>`, not a fill. Conversation unboxed. 1-col `┃` for live execute. Welcome shine demand-gated. |
| Oh My Pi | `…/oh-my-pi` `docs/tui.md` + coding-agent theme | 4-sided `boxRound` composer (rejected for Carina shell). Status in top rule. Tool frames recede when done. |
| Codex CLI | `…/codex/codex-rs/tui/src/style.rs` | User band is a **12% white / 4% black blend of OSC-probed default bg**, not a second surface. 2-col `▌`/`›` gutter. Composer = tinted fill, no drawn box. |
| Claude Code | notes + PromptInput | Open-sided rails. Dashed frames = diffs, not the prompt. Thinking spinner lives **outside** the answer. |
| Jcode | `jcode-tui-style/src/theme.rs` + session picker | Role colors. `user_bg` painted onto **spans plus padding**, not a full-row widget fill. Idle leftover may donut (rejected). |

Carina evidence: production goldens under `crates/carina-tui/src/app/snapshots/`,
toy `tests/snapshots/golden_frames__golden_empty_conversation.snap`,
`styles.md`, `DESIGN.md`, `theme.rs`, `app/render.rs`, and the operator
screenshot of a 4-turn `pebble` session (你好 / 你会啥 / 你好 / 今天天气如何).

---

## Phase 0 — Ruler (rebuilt)

17 extracted color tokens, boxes, status/notice, empty/error, motion. That
ruler was **necessary and insufficient**. It scored empty chat, composer, and
header. It did not score a **normal chat with four short turns**.

### What good actually looks like (roles, not hex)

**Color.** Semantic tokens. Three-step type ramp (text → muted → dim).
Completed work goes gray. One accent owns a component. Codex user fill is a
**tint of the terminal**, never a second painted room. Grok user identity is
a mark color (`accent_user`), not a band. Carina already has the compiler
(`theme.rs`). Spending `user_message_bg` as a full-width slab is a **misuse**,
not a missing token.

**Boxes.** One family. Conversation turns are **not** cards. The prompt is a
signed field. Claude: open-sided rails. Codex: fill + gutter, no border. OMP:
4-sided card (do not copy onto Carina’s Reset-root shell). Grok: left rail.

**Status vs notice.** Live state ≠ toast. Idle may be quiet **if** the
composer is still an object. Jcode split. OMP status-in-top-rule.

**Empty / error / loading.** First open is identity that then sits still.
Errors are a mark plus a sentence. Spinners are one cell and stop. Do not
fill leftover height with a donut.

**Diff / stream / tools.** Answers unboxed. Tools recede when done. Live work
gets one column of state, not a four-sided receipt on every Read. Streaming
is body growth, not a cosine wash of the answer.

**Motion.** Demand-gated. Completed chrome does not sparkle.

**Hierarchy / negative space (the law 17 under-weighted).** Leftover cells
are **blank rest**. Assistant answers are the page. User turns identify the
ask **without becoming table rows**. A 4-turn chat on 80×24 must still look
like a designed document, not a syslog with a pretty footer.

### Phase 0 conclusion

The ruler is:

1. A signed input.
2. A first-open identity that then sits still.
3. Live vs settled as a visual rank.
4. Status you can scan; notices that vanish.
5. Motion only while something is unfinished.
6. One border family.
7. **The daily conversation is the product.** Empty-state and chrome cannot
   rescue a transcript that still looks like an operations ledger.

Carina 0.8.29 owns (1)–(6) on **empty and chrome**. It still fails (7) on
the surface operators live in.

---

## Phase 1 — Inventory (v0.8.29, with working-tree note)

Evidence is goldens + the operator screenshot, not “the feature exists.”

### 1. Startup / empty conversation — **improved, not the daily surface**

**Looks like:** `golden_frames__golden_empty_conversation.snap` 80×24 now
centers the 10×5 braille mark (brand `#de859b`, still drifted from `#8e4053`),
the word `Carina`, and one hint. ASCII/narrow drops the mark. After the first
turn this composition is gone.

**Ugly leftover:** brand hex drift; optional shine not shipped (P1).

**Ruler:** Grok Minimal one-shot card; OMP cached poster. **Pass for empty
only.** Empty is not the session in the screenshot.

### 2. Main transcript / daily chat — **FAIL (primary)**

**Looks like (shipped 0.8.29, operator screenshot):**

```
❯ 你好                          ← full-width muddy slab (shipped)
• 你好，有什么需要我处理的？
❯ 你会啥                        ← another slab
• 我是 Carina…（三段墙）
❯ 今天天气如何
┃ Run  查询北京…  curl  已拒绝  ← danger/warning rail on a closed decision
• 当前运行策略不允许…
                                 ← large leftover void
```

Goldens `visual_density_en_120.snap` (working tree after occupied-band knife):
user band now `(1,0)..(47,0)` on glyphs, not `(3,0)..(119,0)`. **Uncommitted.
Not in the operator binary.**

Assistant body is `Reset` with a dim bullet. Next to full-width user slabs and
a shouted denied Run, the answer still reads as “more log.” Compact related
gap 0. Leftover height is Reset — correct rest — but it **looks unfinished**
while user turns look like UI panels.

**Ugly:** 1 wall (assistant prose + equal-weight tools), 6/8 leftover reads as
hole because slabs fake chrome, 9 denied Run shouts, 12 no hierarchy.

**Ruler:** Grok unboxes answers, rails only live execute. Codex user tint is
12% of terminal bg. Jcode paints `user_bg` on spans. Claude’s empty REPL is
still a product because the **prompt** is signed **and** the transcript is
not a table.

### 3. Prompt / composer — **landed chrome**

**Looks like:** `composer_chrome_en_idle_80.snap` — top **and** bottom `─`
rails, `❯ ` prompt, open sides. Focused rails are ion-cyan (screenshot).
Status lives in the top rail when running.

**Ugly leftover:** paste/file/image chips still `theme.action()` reversed
cyan; chip helpers can still call `Theme::detected(None)`.

**Ruler:** Claude open-sided + OMP status-in-top-rule. **Pass for signature.
Fail for chips.**

### 4. Status / notification — **partial**

Idle: quiet rails, no telemetry dump. Running: spinner + copy in top rail.
Header: `Carina ▾ · pebble 会话` / `openrouter/openai/gpt-5  执行` — no
`reasoning off`. Mode `执行` still competes with the model on the right.

**Ugly:** 5 mild — mode chip vs model; chips shout.

### 5. Tool results — **partial**

Live `┃` + warning title. Settled Read/Explore recede (gallery). **Denied
shell** on 0.8.29 is classified as Failure (`member.is_failure()` includes
`denied`) → danger rail. Working tree reclassifies policy-denied as a
settled Tool. **Uncommitted.**

Expanded tools can still dump 24 lines.

### 6. Diff / `/changes` — **unchanged FAIL**

Transcript `+`/`-` fg-only. Workbench cramped, ellipsis IDs, empty guts.
Toy `golden_diff` still green vs production cyan.

### 7. Error / approval — **landed restraint, denied leak**

Approval overlay warning border. Failure `[ Retry ]` chips. Policy-denied
tool still used the failure skeleton until the working-tree knife.

### 8. Other empties — **FAIL**

Context overlay slang (`ctx` / `ledger` / `hash`). File popup empty box.

### 9. Slash / mentions — **FAIL**

Rounded accent boxes, double cyan on selected row.

### 10. Modals / theme — **A101 landed**

`/theme` + Settings live `auto|dark|light`. No catalog (correct). Symbols
specimen still burns the 3-hue budget as a sample. Doctor `" Doctor "`.
Question hardcoded `>`.

### 11. Streaming — **A007 landed for thinking**

Live thinking `⠋ ` width-locked. Assistant stream is still a growing gray
paragraph. No label shimmer (correctly rejected for the body).

### 12. Header / product menu — **A003 landed**

No reasoning telemetry. Menu vs mode accent competition remains if both
paint.

### 13. Plan review — **least ugly overlay, keymap footer**

### 14. ScreenMode — **geometry only**

Minimal is not a designed native-scrollback look.

### Phase 1 conclusion

0.8.29 made **empty, composer, header, live rail, approval, thinking
spinner, /theme** look designed. The operator’s 4-turn screenshot does not
use those surfaces. Daily chat is still a syslog with a signed footer. That
is an audit miss, not an implementation miss of 17’s P0 list.

---

## Phase 2 — Ugly Patterns (one hit = FAIL)

Scored on **shipped 0.8.29**. Working-tree knives noted.

| # | Pattern | 0.8.29 ship | After working tree | Evidence |
|---|---------|-------------|--------------------|----------|
| 1 | Wall of text | **FAIL** | **FAIL** | Assistant walls; expanded tools; Status/Context dumps |
| 2 | Hardcoded colors | **PARTIAL** | **PARTIAL** | Renderers clean. Chips `Theme::detected(None)`. Brand hex drift |
| 3 | Weak theme | **PARTIAL** | **PARTIAL** | `/theme` exists. No catalog (good). Conversation barely uses brand |
| 4 | Mixed borders | **PARTIAL** | **PARTIAL** | Composer now rails. Overlays rounded. Question `>`. Toy goldens sharp |
| 5 | Chaotic status | **PARTIAL** | **PARTIAL** | Top-rail status. Header quieter. Mode vs model still compete |
| 6 | No breathing | **FAIL** | **FAIL** | Compact related gap 0; leftover height looks dead next to slabs |
| 7 | Flicker | **PARTIAL** | **PARTIAL** | Scheduler + sync output |
| 8 | Empty ugly | **PARTIAL** | **PARTIAL** | Empty identity landed. Daily leftover still reads as unfinished |
| 9 | Screaming errors | **FAIL** | **PARTIAL** | Approval warning landed. Denied Run still a failure cell **until uncommitted knife** |
| 10 | Unsigned prompt | **PASS** | **PASS** | Open-sided rails |
| 11 | ASCII art missing | **PARTIAL** | **PARTIAL** | Mark on empty only. Hidden after first turn (correct). Drifted brand hex |
| 12 | No hierarchy | **FAIL** | **FAIL** | User slab (ship) / user pill (tree) vs assistant vs tools still similar weight |
| 13 | Broken alignment | **FAIL** | **FAIL** | `/changes` ellipsis |
| 14 | Animation undisciplined | **PARTIAL** | **PARTIAL** | Thinking spinner landed. No empty shine. No stream pulse (body shimmer rejected) |

**Shipped 0.8.29: 1 PASS, 7 PARTIAL, 6 FAIL.** Better than 17’s 12 FAIL, still
not a product you open for pleasure. The remaining FAILs are **exactly the
screenshot**.

---

## Phase 3 — Scores

Reference average 8+. Daily conversation weighted hardest.

| Surface | Reference best (source) | Carina 0.8.29 | Score | Ugly | Priority |
|---------|-------------------------|---------------|-------|------|----------|
| **Daily 4-turn chat** | Codex tinted user + unboxed answer; Grok unboxed + live rail only | Full-width user slabs, assistant wall, shouted denied Run, leftover void | **3** | 1, 9, 12 | **P0** |
| Empty / first open | Grok/OMP identity | Centered mark + name + hint | **7** | 11 hex drift | P1 |
| Composer | Claude open-sided + OMP top-rule status | Rails landed; chips shout | **7** | chips | P1 |
| Streaming / thinking | Claude spinner off-body; Codex label shimmer | Thinking `⠋ `; answer still a stuck paragraph | **6** | 14 | P1 |
| Header | One identity + one fact | Model without reasoning | **7** | mode vs model | P1 |
| Error / approval | Mark + sentence | Warning overlay; chips; denied leak | **6** | 9 | **P0** (denied) |
| Tool blocks | Recede when done | Live `┃` good; denied = failure | **6** | 9, 1 | **P0** |
| Status / notice | Jcode split | Top rail | **7** | 5 | P1 |
| Slash | Overlay, one accent | Double cyan | **5** | 12 | P1 |
| Diff / `/changes` | Stable gutters | Ellipsis, empty guts | **5** | 13 | P1 |
| Plan review | Dense keycaps | Keymap novel | **6** | 1 | P1 |
| Settings / theme | Grok `/theme` preview | `auto\|dark\|light` live | **7** | 3 | P2 |
| Motion | Demand-gated | Scheduler honest; shine optional | **6** | 14 | P1 |
| Minimal / NO_COLOR | Grok Minimal Reset | Geometry OK | **6** | — | P2 |
| **First impression of a real session** | 8+ | Syslog with a signed composer | **3** | 12 | **P0** |

**Weighted product score: ~5 / 10. Still FAIL.**

Gap in one sentence: 0.8.29 designed the **frame**. Operators live in the
**picture**.

---

## Phase 4 — Deliverables

### 4.1 Design system

`crates/carina-tui/DESIGN.md` remains the draft law. This refresh amends it:

- Baseline **0.8.29**.
- Scan test **includes** a 4-turn short-chat fixture, not only empty+rails.
- User band = occupied cells only (Codex *tint job*, not Codex pixels).
- Policy-denied tools are settled receipts, not Failure cells.
- Leftover transcript height is rest (`Color::Reset`), never a donut.

Steal jobs, not skins. Still rejected wholesale: GrokNight, OMP π, Claude
terracotta as identity, Jcode donut, Buddy, Codex 36-frame loops.

### 4.2 P0 visual knives (conversation document)

Chrome A001–A007 and A101 are **shipped**. They stay. New P0 is what the
screenshot actually contains.

#### P0-A010 — User turn is a pill, not a table row

- **Problem:** `Paragraph.style(user_message_bg)` filled the whole content
  rectangle. “你好” became an 80-column slab.
- **Reference:** Codex `user_message_bg` is a 4–12% blend of default bg;
  Jcode paints `user_bg` on spans; Grok uses mark color, no fill.
- **Change:** `app/render.rs` paint band on occupied spans only; do not
  patch the outer gutter. `styles.md` already amended in working tree.
- **Accept:** A 2-character user turn leaves ≥20 Reset cells on that row.
  Selection may still own the full hit rectangle. Golden style runs must not
  extend band to the right edge on short copy.
- **Status:** **shipped 0.8.30.** Occupied cells only; short copy is a pill.

#### P0-A012 — Policy-denied tools recede

- **Problem:** `ToolGroupMember::is_failure()` includes `denied`, so
  `SemanticCellKind::Failure` + danger rail. A closed policy decision looked
  like a crash.
- **Reference:** Grok rails **live** execute only. Settled work goes gray.
- **Change:** `semantic_cell.rs` exclude policy-denied from Failure;
  settled tool title when `!is_live_work()`.
- **Accept:** Denied Run has no accent rail, no danger fg. Status word
  `已拒绝` / `denied` remains. Real `FailurePresentation` cells keep `✗` +
  chips + rail.
- **Status:** **shipped 0.8.30.** Denied Run is a settled receipt, not Failure.

#### P0-A011 — Assistant answer is the page

- **Problem:** After user slabs and a shouted tool, the answer is just
  another gray block. Compact related-gap 0 glues ask/answer/tool into one
  log.
- **Reference:** Grok/Claude unboxed answers; tools recede; leftover is
  blank.
- **Change:** Keep body `Reset`. Do not gray the answer to match thinking.
  After A010/A012 land, re-score; if the wall remains, add **one** extra gap
  after an assistant turn before a settled tool (presentation only, not
  reducer identity).
- **Accept:** In a 4-turn fixture, a trained operator names the answer
  without hunting. Tools quieter than the answer.
- **Status:** **shipped 0.8.30.** Extra beat after the answer; settled tool
  titles use muted `gray`, not `Reset` / bold. Body stays `Reset`.

#### P0-A013 — Leftover height is rest

- **Problem:** Blank cells under the last turn look like missing UI while
  user turns look like panels.
- **Reference:** Good TUIs spend leftover as blank. Jcode donut rejected.
- **Change:** No filler widget. After A010 the void should read as rest.
  Document in `styles.md` (working tree). Do not invent a footer poster.
- **Accept:** No idle donut, no second empty identity after the first turn.
- **Status:** **shipped 0.8.30.** Leftover cells stay `Color::Reset`.

Do **not** wait for a theme catalog. Do **not** reopen A001–A007 unless a
regression shows.

### 4.3 P1 / P2

**P1** (from 17, still open, lower than A010–A013):

| ID | Item |
|----|------|
| A102 | Optional one-shot empty shine (12 fps, 4 s, park). |
| A103 | `/changes` measure; no 4-char ellipsis IDs; transcript vs workbench palettes. |
| A104 | Plan footer: one keycap row. |
| A105 | Question list `glyphs.selected()`. Doctor title through i18n. |
| A106 | Slash selected row **or** accent name, not both. |
| A107 | Paste chips as muted pills; live `Theme`, never `Theme::detected(None)`. |
| A108 | Context/Status: VOICE, no `ledger`/`hash`/`ctx`. |
| A109 | Toy `golden_frames` aligned with production or deleted. |
| A114 | Dark `Palette.brand` vs `#8e4053` — still contrast-blocked on void; do not ship unreadable rose. |

**P2:** extra palettes from mineral tokens; designed Minimal card; tmux/SSH
goldens; optional thinking glyph extra `· ✢ ✳ ✶`.

### 4.4 Anti-patterns — never again

1. Do not treat “typed cells / goldens / feature wired” as an aesthetic pass.
2. Do not treat **empty-state and composer chrome** as the daily product.
3. Do not copy GrokNight, TokyoNight, OMP π, Claude terracotta identity, or
   Jcode donut/Buddy.
4. Do not paint brand-rose on buttons, composer, or assistant text.
5. Do not invent a second token set.
6. Do not mix dashed Claude boxes with rounded OMP boxes on the same shell.
7. Do not box every tool. Rail live work; frame overlays and the composer only.
8. Do not loop welcome animation on every empty chat or every new turn.
9. Do not put provider/reasoning/ctx/ledger on the idle header.
10. Do not use full-frame danger fills. Danger is a mark.
11. Do not shimmer assistant bodies.
12. Do not call `Theme::detected(None)` from a widget that has `self.theme`.
13. Do not hardcode `>`, `" Doctor "`, or protocol tails in chrome.
14. Do not reopen kernel / audit / transactional-patch / yolo / MiniLM /
    ACP-as-UI to “make it pretty.”
15. **Do not ship a P0 that an operator 4-turn screenshot cannot see.**

---

## Acceptance (aesthetic)

A trained operator, 3 seconds, 80×24, fixture of **four short user turns +
one denied Run + one assistant answer**:

1. Can point at what they asked (user pills, not table rows).
2. Can point at the answer (unboxed, louder than tools).
3. Can see the denied command without reading it as a crash.
4. Does not think the leftover height is a missing widget.

Empty identity, composer rails, header, live thinking spinner, and `/theme`
must **remain**. They are not sufficient.

Functional and still ugly remains **FAIL**.
