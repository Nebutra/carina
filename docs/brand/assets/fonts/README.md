# Carina Display Alpha

Experimental brand-display font derived from the accepted CARINA wordmark.

- Use for the Carina name and large identity-led headings.
- Do not use for body copy, controls, code, audit data, CLI/TUI output, or accessibility-critical text.
- Character set: A-Z, a-z, and space.
- Missing production work: numerals, punctuation, diacritics, manual kerning, hinting, and final Bézier refinement.

The TTF and WOFF2 are project assets covered by the repository license. SVG glyph sources are split
between `glyphs-svg/uppercase/` and `glyphs-svg/lowercase/` to avoid case-collision on common macOS filesystems.
Consumption-ready 512px PNG cards are under the matching `glyphs-png/` directories. They are optimized
4-bit grayscale derivatives; use SVG when scaling, recoloring, or editing outlines.

`fonts.css` registers Carina Display Alpha plus the bundled Geist Sans/Mono variable WOFF2 files.
The Geist assets retain their original license texts in this directory. `carina-display-alpha.css`
is the smaller identity-font-only entry point.
