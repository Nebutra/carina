# Carina Brand Package

This directory is the repository-owned, production-consumable subset of the Carina Design System.
It intentionally excludes generation rounds, rejected candidates, mockups, logo-skin experiments,
and the original 32 MB glyph PNG cards. Lightweight 512px, 16-level grayscale derivatives are included for direct raster consumption; SVG remains canonical.

## Contents

- `assets/logo/`: accepted symbol, wordmark, lockups, sprite, and raster fallbacks.
- `assets/fonts/`: Carina Display Alpha, licensed Geist Sans/Mono WOFF2 files, CSS registrations, 52 SVG glyph sources, and 52 optimized PNG derivatives.
- `assets/hero/`: optimized README media derived from an approved foundation composition.
- `assets/specimens/`: evidence rendered from the compiled font; not identity masters.
- `design-system/`: extended DTCG tokens plus CSS, Tailwind v4, and Astryx adapters.
- `brand-brief.md`: strategy, naming, voice, and terminal brand contract.
- `asset-manifest.json`: generated inventory and checksums.
- `AGENTS.md`: mandatory consumption and maintenance rules for coding agents.

## Source Provenance

Imported from the Carina Design System development workspace on 2026-07-16.
The repository copy is now the source for product consumption. The original workspace remains a
design-development archive and must not be referenced by code, docs, manifests, or automation.

The identity masters originated from the accepted `brand-vi/svg/` outputs. The design tokens came
from the `extended/` profile and were reconciled with the accepted mark and Carina Display Alpha.

## Verification

```bash
make brand-check
```

To record an approved asset change:

```bash
python3 scripts/brand_assets.py --update
make brand-check
```
