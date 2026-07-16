# Carina Brand Asset Rules

Scope: `docs/brand/` and every repository surface consuming these assets.

## Authority Order

1. `assets/logo/carina-symbol.svg` and `assets/logo/carina-wordmark.svg` are the accepted identity masters.
2. `design-system/design-tokens.json` is the machine-readable color, typography, spacing, radius, and motion source.
3. `design-system/DESIGN.md` explains how to apply those tokens.
4. `brand-brief.md` owns product meaning, voice, naming, and terminal-specific intent.
5. `assets/hero/`, `assets/specimens/`, README media, and integration icons are derivatives, not identity sources.

When files disagree, do not blend them. Fix the lower-authority consumer to match the higher-authority source.

## Identity

- Preserve the accepted symbol silhouette, rotation, central counterform, and approved wordmark outlines.
- Use `#8e4053` (`brand-rose`) for the canonical colored symbol. Use monochrome `currentColor` when a host controls icon color, including VS Code.
- Use the supplied horizontal or stacked lockup. Do not typeset `CARINA` with a substitute font to recreate the wordmark.
- Do not add gradients, shadows, materials, outlines, animation, or seasonal skins to the canonical masters.
- Do not use skin, mockup, or specimen files as a source for tracing.

## Typography

- `Carina Display Alpha` is for brand names, display headlines, and composed identity moments only.
- The Alpha contains A-Z, a-z, and space. It does not yet contain numerals, punctuation, diacritics, production kerning, or manual hinting.
- Use Geist Sans or the system sans stack for UI, documentation body copy, controls, navigation, and tables.
- Use Geist Mono or the system mono stack for code, audit records, hashes, paths, policy names, timestamps, and CLI/TUI content.
- Never introduce Newsreader or another font as a replacement source for the CARINA wordmark.
- SVG glyphs are canonical. The 512px PNG glyph cards are optimized raster derivatives and must not be traced back into the font.

## Color Roles

- `brand-rose` identifies the mark. It is not the default interaction color.
- `ion-cyan` is the primary product interaction and focus signal.
- Green, amber, red, blue, and violet are semantic/capability signals. Always pair status color with text or an icon.
- Use semantic tokens in product code. Do not hardcode brand hex values inside individual views.
- Preserve `NO_COLOR` and monochrome terminal behavior.

## Consumption Map

- Three README files consume `assets/hero/carina-readme-hero.webp` and the approved badge palette.
- `integrations/vscode/media/carina.svg` is a byte-identical monochrome derivative of `assets/logo/carina-symbol.svg`.
- `go/tui/theme` transcribes the terminal subset of `design-system/design-tokens.json` plus documented ANSI-256 fallbacks.
- Future web/UI clients should consume `design-system/variables.css`, `design-system/tailwind-v4.css`, or `design-system/carina.ts`; do not maintain a second token set.

## Change Procedure

1. Change the highest-authority source first.
2. Update intentional derivatives and consumer references.
3. Run `python3 scripts/brand_assets.py --update` only when the changed asset is approved.
4. Run `make brand-check` and the tests for every touched consumer.
5. Visually inspect the README hero, the symbol at 16-48 px, and compiled font specimens when relevant.

Do not edit `asset-manifest.json` by hand. The update command records the explicit inventory,
byte size, and SHA-256 digest. Unlisted files are not approved production assets.
