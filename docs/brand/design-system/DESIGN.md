# Carina Design System

> Spectral Mineral: a precision runtime interface derived from mineral dust, ionized edges, and observable energy.

## 1. Brand Thesis

Carina is an AI Agent Runtime. Its visual language should communicate controlled energy rather than generic science fiction. The Carina Nebula supplies the metaphor: thousands of luminous bodies forming inside a large, turbulent system, made legible through observation.

The system is built on three ideas:

1. **Formation**: agents, workflows, and tools emerge from reusable runtime primitives.
2. **Orchestration**: many independent bodies remain coordinated inside one field.
3. **Observation**: every action produces a readable trace, state, and consequence.

The result is dark, exact, and operational. The nebula is translated into a low-chroma mineral field with sparse high-energy color. Color is used as signal taxonomy, not wallpaper.

The accepted identity adds one deliberately separate brand primitive: `brand-rose` (`#8e4053`) for the Carina symbol. It identifies the mark; it does not replace ion cyan as the product interaction color.

## 2. What Changes From Perk

### Retain

- One dominant brand accent at a time.
- A single geometric sans family for most interface text.
- Flat hierarchy: borders, tonal steps, and spacing instead of shadows.
- Strong display typography paired with compact utility labels.
- A 4px spatial base unit.

### Replace

- Electric lime becomes **ion cyan**, a highly visible primary interaction signal taken from the nebula's cool luminous edges.
- Warm parchment becomes **carbon void** and **lunar mineral** surfaces.
- 28px lifestyle-card radii become 8px operational containers.
- Soft travel-editorial composition becomes dense, scan-friendly runtime UI.
- Decorative accent panels become semantic states, traces, and capability markers.

## 3. Voice

Carina speaks like an observatory control room: concise, factual, and calm under load.

- Prefer: `Run started`, `3 agents active`, `Trace retained for 7 days`.
- Avoid: playful filler, cosmic puns, exaggerated intelligence claims, and mystical language in product UI.
- Marketing may use the formation metaphor; operational UI should name concrete states.

## 4. Color System

### Method, Not Eyedropper

The source image was reduced to 14 dominant clusters and measured in OKLCH. Most image area falls between hue 52-110 degrees with low chroma (`C 0.013-0.084`): smoke gray, ochre, dust brown, and mineral ivory. The cyan regions are visually salient but occupy less area. The UI therefore uses:

- low-chroma neutrals for 80-90% of the interface;
- ion cyan as the primary action and focus family;
- copper amber as its warm complementary counterweight for brand emphasis;
- separate green, amber, red, blue, and violet state/capability colors with matched perceived lightness.

Color ramps are authored in OKLCH because its lightness channel tracks perceived brightness more consistently than HSL. Hex fallbacks are supplied for tooling compatibility.

Adobe Color harmony is used only as a composition tool: Carina uses a cool cyan/teal anchor with a warm copper-orange complementary axis, plus low-chroma analogous blue-green support. Harmony does not determine text legibility.

Legibility is validated independently using WCAG 2.2 relative-luminance contrast:

- normal text: at least `4.5:1`;
- large text: at least `3:1`;
- UI boundaries and focus indicators: at least `3:1` against adjacent colors;
- preferred body text target: `7:1` or higher on primary surfaces.

### Foundations

| Token | Dark | Light | Role |
| --- | --- | --- | --- |
| `void` | `#0d1214` | `#f5f3ed` | Page canvas |
| `surface` | `#141b1d` | `#fffdf8` | Primary working surface |
| `surface-raised` | `#1c2527` | `#eceae3` | Popovers, selected rows, code blocks |
| `border` | `#344144` | `#cfd3ce` | Hairlines and boundaries |
| `text-primary` | `#f3f0e8` | `#182023` | Primary copy |
| `text-secondary` | `#b0b7b3` | `#5d6868` | Supporting copy |

### Emission Signals

| Signal | Value | Meaning |
| --- | --- | --- |
| Ion cyan | `#8edbd2` | Primary action and selected runtime object |
| Ion cyan bright | `#afe9e3` | Hover/highlight on dark surfaces |
| Dust violet | `#c6a6ea` | Agents and delegated execution |
| Oxygen blue | `#78bff2` | Memory, data, retrieval |
| Copper amber | `#e8a85f` | Brand counterpoint and tool activity |
| Spectral green | `#68d2a3` | Healthy, observable, completed |
| Event red | `#ff7c78` | Error, destructive, failed |

Only one emission signal should dominate a component. Multi-color nebula gradients are reserved for brand moments and loading/formation states, never data encoding. Status always includes an icon and label so red/green confusion cannot hide meaning.

## 5. Typography

### Families

- Brand name and identity lines: `Carina Display Alpha`, regular; fallback `Georgia`, serif. The Alpha is derived from the accepted CARINA wordmark and is not a general heading or body font.
- Product headings: `Geist Sans`, weight 500-600; fallback system sans. This role supports punctuation, numerals, localization, and operational UI.
- UI, body, labels, and navigation: `Geist Sans`, weight 400-600; fallback system sans.
- Code and runtime data: `Geist Mono`, weight 400-600; fallback `SFMono-Regular`.
- All three Latin variable WOFF2 assets and their licenses ship in `assets/fonts/`.

### Scale

| Role | Size | Leading | Weight |
| --- | ---: | ---: | ---: |
| brand | 72px | 1.0 | 400 |
| brand-xl | 120px | 0.94 | 400 |
| micro | 11px | 1.45 | 500 |
| caption | 12px | 1.5 | 400 |
| body-sm | 14px | 1.5 | 400 |
| body | 16px | 1.5 | 400 |
| title-sm | 20px | 1.22 | 600 |
| title | 28px | 1.14 | 600 |
| heading | 40px | 1.06 | 600 |
| display | 72px | 1.0 | 500 |
| display-xl | 120px | 0.94 | 500 |

Use normal letter spacing for Carina Display Alpha. Reserve it for the Carina name and short pure-Latin identity lines because the Alpha does not contain numerals, punctuation, or localized scripts. Product headings, controls, navigation, tables, metrics, and long body copy remain Geist Sans. Uppercase telemetry labels use Geist Sans at 11-12px with `0.08em` tracking.

## 6. Shape And Layout

- Base spacing unit: 4px.
- Application max width: 1440px.
- Reading max width: 720px.
- Navigation height: 56px.
- Control heights: 32px compact, 40px default, 48px large.
- Cards and panels: 8px radius.
- Inputs and buttons: 6px radius.
- Tags: 4px radius; pills only for status filters or presence.
- Borders: 1px. No drop shadows in normal layout.

Operational pages use persistent navigation, split panes, tables, timelines, and inspectors. Marketing pages may use edge-bleeding type and one photographic nebula field, but the product UI remains quiet.

## 7. Component Principles

### Buttons

- Primary: ion-cyan fill, carbon-void text, one per action cluster. Contrast is `11.51:1`.
- Secondary: surface fill with border.
- Ghost: transparent, used for toolbar actions.
- Destructive: event red only when the action is irreversible.
- Icon-only buttons use familiar symbols and an accessible tooltip.

### Cards And Panels

Cards represent discrete objects such as an agent, run, workflow, or integration. Page sections are not cards. Nested cards are prohibited; use dividers and surface steps inside inspectors.

### Status

Status always combines color with text or icon:

- running: ion-cyan pulse + `Running`
- queued: copper amber + `Queued`
- healthy/completed: spectral green + `Healthy` or `Completed`
- failed: event red + `Failed`
- paused: neutral violet-gray + `Paused`

### Data Visualization

Use a dark neutral grid, direct labels, and no more than five series colors. Ion cyan is reserved for selected or live data. Never use a nebula gradient as a quantitative scale. For ordered data, vary lightness monotonically in one hue family; for categories, use equal-lightness hues plus direct labels.

### Code And Traces

Code surfaces use `surface-raised`, 8px radius, and Geist Mono. Trace rows use fixed columns for timestamp, actor, event, latency, and status. Dense modes may reduce spacing, not type below 12px.

## 8. Motion

- Fast: 120ms for hover and press.
- Medium: 220ms for panels and disclosure.
- Slow: 480ms for formation/loading sequences.
- Easing: `cubic-bezier(0.2, 0, 0, 1)`.
- No ambient floating UI. Nebula motion is limited to hero media and loading illustrations.
- Respect `prefers-reduced-motion` and remove glow drift or pulsing.

## 9. Accessibility

- Body text targets WCAG AA contrast at minimum.
- Focus rings use a 2px ion-cyan outer ring with 2px offset.
- Color never carries state alone; icons, text, line patterns, or shapes provide a second channel.
- Validate protanopia, deuteranopia, and tritanopia simulations before release.
- Minimum target size is 40x40px, except dense desktop toolbars where 32x32px is acceptable with spacing and tooltips.
- Heading levels follow document structure even when visual sizes differ.

## 10. Imagery

Use authentic astronomical imagery for first-viewport brand moments. Crop around dark dust lanes and luminous emission ridges, leaving quiet negative space for type. Product imagery shows the real runtime interface without device frames. Do not use generic planets, astronauts, glowing brains, HUD circles, or random starfield patterns.

## 11. Astryx Mapping

Carina extends `@astryxdesign/theme-neutral` and overrides semantic tokens through `defineTheme()`. Use Astryx components and semantic props before custom DOM. Import components from documented subpaths, for example `@astryxdesign/core/Button`.

For production SSR builds:

```bash
npx astryx theme build ./src/themes/carina.ts
```

Import the generated theme object and CSS, then wrap the application with `Theme`. Generate current agent instructions with:

```bash
npx astryx init --features agents --agent codex
```

Reference: https://astryx.atmeta.com/docs/getting-started
