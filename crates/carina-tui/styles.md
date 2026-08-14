# Terminal style contract

Carina treats the terminal as its substrate, not a canvas. The root frame uses
the terminal's default foreground and background (`Color::Reset`). Transparent,
blurred, light, and dark profiles therefore remain owned by the user.

## Semantic ownership

All product color is defined in `src/theme.rs`. Renderers may consume semantic
fields and style helpers, but must not construct RGB, indexed, or named ANSI
colors. Clippy enforces this rule. A protocol value that happens to use a color
cell, such as Kitty's image placeholder ID, needs a local `#[allow]` with a
reason explaining why it is not visual styling.

The palette owns three neutral levels (`gray_dim`, `gray`, `gray_bright`),
interaction and status colors, tool/spinner/link styles, selection, relative
user-message and code backgrounds, diff foreground/background/gutter colors,
and all six Markdown heading colors and modifiers. `muted()` and `dim()` fall
back to `Reset + DIM`, which remains readable on either polarity.

Background fills are intentionally rare. Only selection, user-message
separation, code, and diff regions may use them. Panels and overlays use
borders, spacing, DIM, and focus styles instead of opaque surfaces.

## Detection and fallback

`CARINA_THEME=dark|light|auto` owns polarity. Auto mode starts with the fast
`COLORFGBG` signal and accepts a bounded OSC 11 result collected before the
event loop. Failure never delays launch beyond the probe deadline.

The selected palette is quantized once at startup to `TrueColor`, `Ansi256`,
`Basic`, or `None`. ANSI-256 uses CIE76 distance in Lab space. Basic mode drops
all backgrounds, including diff backgrounds, and uses terminal-defined accents;
secondary text becomes `Reset + DIM`, never `DarkGray`. No-color adds structural
emphasis such as reverse video for selection.

Carina's neutral default palette was chosen because its gray hierarchy and
cyan/green/red semantics survive xterm-256 quantization without turning into an
unrelated blue cast. This fallback is a first-class design target, not an
approximation of a dark truecolor screenshot.

## Transcript grammar

User and assistant messages share one responsive styled role-prefix grammar. The
prefix is part of the wrap input, not inserted after wrapping: a two-cell
semantic mark, a six-cell locale-owned role label, and a two-cell relationship
gap produce the same ten-cell first-line prefix for both roles. At 72 content
cells and above, continuation rows retain that ten-cell hanging indent. Below 72
cells, continuation rows retain only the two-cell semantic rail so the role does
not consume a disproportionate share of the reading measure. All seven locale
labels must fit the six-cell field. Extreme-width degradation may clip the
prefix to preserve one body cell; it must never drop or rewrite message
graphemes. Long URLs wrap at grapheme boundaries without inserted whitespace,
hyphens, or mutated bytes.
Production rendering and golden frames share `transcript_content`, which applies
the `TRANSCRIPT_READING_MAX_WIDTH` measure instead of the wider product canvas.

Expanded tool detail uses the semantic two-cell tree gutter on every source and
continuation row. Related operational rows (tool/tool and tool/thinking in either
order) use the related-row gap; unrelated conversation objects use the normal
block gap. Selection chrome remains outside the content rectangle. Pointer hover
may patch the semantic interaction style onto a collapsible header, but it must
not change wrapping, retained heights, visible copy, or pointer hit geometry.
The last pointer position is re-resolved after every frame rebuild so hover can
leave and re-enter repeatedly and cannot retain stale ownership after reflow,
scrolling, disclosure, resize, or overlay changes.

## Density projection

Transcript density is a persisted presentation preference over one semantic
tree. `compact` is the default; `comfortable` adds space and bounded previews
without changing disclosure state. `/density` and the settings row dispatch the same action and
persist `tui_density` without replacing unrelated configuration keys.

| Projection | Compact | Comfortable |
|------------|---------|-------------|
| unrelated / related / final gap | 1 / 0 / 1 | 2 / 1 / 2 |
| default routine tool detail | collapsed | collapsed |
| collapsed group members | 0 | 2 |
| collapsed plain-output lines | 0 | 3 |
| collapsed create preview lines | 4 | 8 |

An explicit disclosure choice always overrides the density default for the
current run. Density must not change transcript or lifecycle identity, draft,
selection, ScreenMode, approval state, or daemon events. Patch summaries,
failures, approvals, and their recovery actions remain visible in both modes.
Expanded plain output stays complete until a separate full-output viewer exists;
density may bound a collapsed preview but may not hide evidence without an
escape hatch.

## Symbol projection

Terminal symbols are a persisted presentation preference over the same semantic
tree. `tui_glyphs` accepts `auto`, `unicode`, `nerd`, or `ascii`; `/symbols` and
the Settings row open the same reversible live preview. Previewing or applying a
choice may change glyph shapes, but it must not change transcript identity,
drafts, selection, disclosure, ScreenMode, or hit geometry.

The renderer consumes one width-locked Unicode, Nerd Font, or ASCII registry.
Automatic uses Unicode except for established legacy-terminal safety signals.
It does not probe installed fonts and never selects Nerd Font; Nerd Font is an
explicit choice that requires Nerd Font Mono. Boxes or misaligned symbols in the
preview indicate missing font coverage and make ASCII the recovery path.

`CARINA_TUI_GLYPHS` is the explicit symbol override and the legacy
`CARINA_ASCII` switch remains a hard ASCII override. The Settings preview must
disclose when an environment override owns the active tier. `NO_COLOR` controls
color capability independently and must not force ASCII. Product chrome stays
rounded in Unicode and Nerd Font modes and plain in ASCII; Markdown tables are
the only sharp-junction exception in graphical modes.

## Composer chrome

The conversation shell is intentionally quieter than setup. Its header owns one
content row and one bottom hairline: a two-cell canonical mini mark followed by
`Carina ▾ · conversation` on the left, with the current mode as the
highest-priority routine action on the right. The mark, product name, and
disclosure form one width-locked product trigger that opens an anchored menu for
New conversation, Conversations, Status, Settings, and Help. Below 24 cells the
mark yields its three cells while the textual product trigger remains available.
A paused run's Review/Resume action
outranks the mode, followed by the model picker. Provider, reasoning, source,
workspace, navigation, and shortcut tutorials remain available in their
dedicated surfaces instead of being repeated above every turn. Hover/open state
may change action style, never label, width, order, or hit geometry.

The product menu is a non-governance overlay anchored below the header hairline,
so its complete border remains visually independent from the shell separator. It
re-registers the header trigger inside overlay hit ownership, captures its
border, and registers only the four outside rectangles as close actions;
background transcript and composer actions remain unreachable. Approval,
question, and plan-review overlays preempt and discard it. If resize, Inline
mode, or a short terminal removes the rendered header anchor, the menu closes
rather than inventing geometry.

The composer is a bottom-anchored input track, not a form card. A single top
hairline separates it from status chrome; a width-locked two-cell prompt glyph
owns the leading column and the TextArea owns the remaining rectangle for
rendering, wrapping, cursor placement, element hit-testing, and mouse input.
Empty input consumes one content row and drafts grow upward to five. The full
track focuses the composer, while clicks inside the TextArea must also place the
cursor and composer drag/up events must complete selection. Focus remains
structural in `NO_COLOR` through bold/reversed prompt treatment.

Conversation state uses a state-derived zero-, one-, or two-row chrome directly
above the bottom-anchored composer. Idle is quiet. Active work owns a narrative
row for the current activity's elapsed time and interrupt hint, plus a compact
Run/Queue row. Waiting, paused, failed, and unknown states use only the status
row. A notice replaces routine narrative/status copy; a priority runtime notice
uses the danger tone. Chrome changes compress or restore transcript height but
never move the composer.

Slots are unboxed semantic text runs separated by the canonical glyph separator.
They use terminal-transparent backgrounds and existing muted, accent, warning,
and danger tokens. Nonzero Queue and warning/critical Context are protected at
every supported width. Routine HITL, Isolation, Context, and ScreenMode remain
available from Settings/Status instead of occupying idle conversation rows.
Protected slots compact before truncation, and display-cell truncation must not
split graphemes.

The Queue slot contains depth only and owns an exact hit region. Pointer and
`/queue` dispatch the same action; hover changes style without moving text or the
hit rectangle. Unknown lifecycle and policy values become localized Unknown
copy, never raw protocol strings. ASCII, no-color, polarity, and ScreenMode may
change glyphs or styling but cannot change slot meaning or action geometry.

Run elapsed and activity elapsed are separate clocks. A newly started activity
never inherits the age of its run, and completing the newest parallel activity
restores the prior live activity with its own elapsed value. The header likewise
separates runtime route truth from next-turn picker preference. Running/paused
and Next labels are owned by the model field once; reasoning shows values in the
same order without repeating those labels. Provider uses runtime facts first,
then inference from the running model, and otherwise labels only the Next route.

## Motion

Motion communicates liveness; it is not decoration. Activity such as a running
execution advances at the shared 33ms scheduler/glyph cadence. Secondary status
work such as provider validation advances at 80ms. Activity takes precedence
when both demand classes are present, and the cadence constants that schedule a
frame are the same constants that select its glyph.

Idle, completed, failed, paused, approval-waiting, input-waiting, and unknown
execution states do not request animation. Governance controls, borders, focus,
hover, and selection remain static. Losing terminal focus or enabling
`CARINA_REDUCED_MOTION` removes every decorative deadline while preserving
semantic event redraws; all animated glyph classes then render their stable
first frame. A late wake draws once and schedules from the observed time rather
than replaying missed frames.

## Patch review workbench

Fullscreen `/changes` uses one product-width outer frame with three stable,
unboxed regions: transaction history, the selected transaction's file tree, and
the selected file's numbered hunk detail. A 120-column terminal is the first
three-region layout; narrower terminals stack the same semantic projection.
Minimal and Inline retain the compact overlay and native-scrollback contract.

The transaction row and summary always show a localized lifecycle disposition;
color is reinforcement, never the identity. Only the focused list uses the
selection background. The other selected list keeps a structural marker and
bold text, preventing two adjacent selected rows from becoming one visual band.
File statistics align at the trailing edge, while hunk detail omits duplicate
`diff --git` path headers and retains old/new line numbers plus bounded word
emphasis.

Patch review consumes the background-built `PatchReview` projection. Frames do
not split or highlight raw patch payloads. The projection is bounded to 2 MiB
and 50,000 logical lines, and any continuation says that the complete typed
transaction remains available for verification and rollback. Fullscreen review
uses no more than three saturated semantic foregrounds at once: interaction/add
cyan, warning amber, and remove/failure red.

Rollback confirmation freezes list navigation and pointer selection. Its short
impact line, affected-file actions, and keycap footer all refer to the previewed
patch and transaction identity. A later list selection cannot retarget the
confirmation; a changed workspace or mismatched identity makes the preview
ineligible.

## Transcript color budget

The main transcript uses no more than three saturated foreground hues at once:

- `ion-cyan`: assistant identity, links, headings, and added-line emphasis;
- `copper-amber`: active tool, review, and warning emphasis; settled routine
  tool receipts return to `gray_bright` so completed operations do not compete
  with live work or risk;
- `event-red`: governance, diagnostics, destructive/error state, and removals.

User identity is `gray_bright`, never the interaction accent. Assistant identity
must never use `spectral-green`; green remains available to non-transcript success
state. Metadata uses `gray_dim`, the quietest neutral step. Thinking uses the same
neutral hierarchy with `DIM | ITALIC`; basic/no-color fallback is
`Reset + DIM | ITALIC`. The transcript theme test deduplicates all chromatic
foreground helpers and fails above three hues, while the style-discipline test
prevents transcript renderers from bypassing those helpers.
