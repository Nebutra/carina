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

User and assistant messages share one styled role-prefix grammar. The prefix is
part of the wrap input, not inserted after wrapping: a two-cell semantic mark,
a six-cell locale-owned role label, and a two-cell relationship gap produce the
same ten-cell hanging indent for both roles. All seven locale labels must fit the
six-cell field. Narrow degradation may clip the prefix to preserve one body cell;
it must never drop or rewrite message graphemes. Long URLs wrap at grapheme
boundaries without inserted whitespace, hyphens, or mutated bytes.
Production rendering and golden frames share `transcript_content`, which applies
the `TRANSCRIPT_READING_MAX_WIDTH` measure instead of the wider product canvas.

Expanded tool detail uses the semantic two-cell tree gutter on every source and
continuation row. Related operational rows (tool/tool and tool/thinking in either
order) use the related-row gap; unrelated conversation objects use the normal
block gap. Selection and hover chrome remain outside the content rectangle, so
neither state changes wrapping, retained heights, nor pointer hit geometry.

## Density projection

Transcript density is a persisted presentation preference over one semantic
tree. `compact` is the default; `comfortable` adds space and opens routine tool
detail by default. `/density` and the settings row dispatch the same action and
persist `tui_density` without replacing unrelated configuration keys.

| Projection | Compact | Comfortable |
|------------|---------|-------------|
| unrelated / related / final gap | 1 / 0 / 1 | 2 / 1 / 2 |
| default routine tool detail | collapsed | expanded |
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

## Composer chrome

Conversation state uses one fixed two-row chrome below the composer. The first
row has one typed narrative owner: active activity with elapsed time and the
interrupt hint may carry a routine notice at its tail, while a priority failure
replaces the narrative. The second row is the only owner of Run, Queue, HITL,
Isolation, Context, and ScreenMode state. Starting work or showing a notice must
never add a row or reduce transcript height.

Slots are unboxed semantic text runs separated by the canonical glyph separator.
They use terminal-transparent backgrounds and existing muted, accent, warning,
and danger tokens. Run, HITL, and Isolation remain visible at every supported
width; nonzero Queue is also protected. Normal Context appears from 80 columns,
ScreenMode from 120, while warning or critical Context remains protected even at
60. Protected slots compact before truncation, and display-cell truncation must
not split graphemes.

The Queue slot contains depth only and owns an exact hit region. Pointer and
`/queue` dispatch the same action; hover changes style without moving text or the
hit rectangle. Unknown lifecycle and policy values become localized Unknown
copy, never raw protocol strings. ASCII, no-color, polarity, and ScreenMode may
change glyphs or styling but cannot change slot meaning or action geometry.

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
