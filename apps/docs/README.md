# Carina Docs

Enterprise-grade documentation site for **Carina** — the local-first AI agent runtime.

Built with **Astro 7 + Starlight**, **Tailwind CSS v4**, and the **Carina design system** (`docs/brand/`).

## Quick start

```bash
cd apps/docs
pnpm install
pnpm sync-brand   # pull tokens + fonts from docs/brand
pnpm dev          # http://localhost:4321
```

## Scripts

| Command | Description |
| --- | --- |
| `pnpm dev` | Local dev server with HMR |
| `pnpm build` | Static production build → `dist/` |
| `pnpm preview` | Preview the production build |
| `pnpm sync-brand` | Re-sync design tokens, fonts, logos from `docs/brand/` |
| `pnpm sync-protocol` | Re-sync JSON-RPC catalog from `protocol/jsonrpc/methods.json` |
| `pnpm sync` | Brand + protocol sync |
| `pnpm typecheck` | Astro + TypeScript checks |

## Architecture

```
apps/docs/
├── public/                 # Static assets (fonts, logos, favicon)
├── scripts/sync-brand.mjs  # Brand authority → package copy
├── src/
│   ├── components/
│   │   ├── landing/        # Hero, FeatureGrid, CodePreview, QuickStart
│   │   ├── layouts/        # ApiReference three-column template
│   │   └── ui/             # CategoryTag, Callout, CodeBlock, VersionSelector
│   ├── content/
│   │   ├── docs/           # EN (root) + zh-cn/ content
│   │   └── i18n/           # UI string catalogs
│   ├── styles/
│   │   ├── brand/          # Synced CSS variables (do not hand-edit long-term)
│   │   ├── fonts.css
│   │   ├── starlight-theme.css  # --sl-* ← Carina tokens
│   │   ├── global.css           # Tailwind v4 + @theme bridge
│   │   └── ux.css               # Scrollbar, search, sidebar polish
│   └── content.config.ts
├── astro.config.mjs
└── vercel.json
```

### Theme / design-system integration (one-click swap)

| Layer | File | Role |
| --- | --- | --- |
| 0 Authority | `docs/brand/design-system/variables.css` | Single source of truth (see `docs/brand/AGENTS.md`) |
| 1 Sync | `pnpm sync-brand` | Copies authority → `src/styles/brand/` |
| 2 Public API | `src/styles/docs-tokens.css` | Stable `--docs-*` contract for **all** components |
| 3 Starlight | `src/styles/starlight-theme.css` | Maps `--sl-*` → `--docs-*` only |
| 4 Tailwind | `src/styles/global.css` | `@theme` bridge, no hex |
| 5 UX | `src/styles/ux.css` | Chrome density, motion, search |

**One-click swap after a brand change:**

```bash
pnpm sync-brand && pnpm build
```

**Contract rules:**

- Components may read `--docs-*` and brand semantic tokens (`--color-*`, `--spacing-*`, …).
- Components must **not** hardcode hex or invent new primitive colors.
- Components must **not** read Starlight `--sl-*` (chrome mapping only).
- Dark mode is **forced** via `ThemeProvider` / `ThemeSelect` overrides.

### Typography roles (do not improvise)

| Role | Family | Allowed use |
| --- | --- | --- |
| Brand identity | `Carina Display Alpha` | Large pure-Latin product name only (A–Z/a–z/space). Prefer logo SVG for small chrome. |
| UI / headings / body | `Geist Sans` | All product UI, doc headings, nav, tables. Weight 400–600. |
| Code / audit | `Geist Mono` | Code blocks, hashes, paths, CLI. |

- `@theme --font-sans` / `--font-mono` use **concrete** stacks (required by starlight-tailwind).
- CJK falls through to PingFang SC / Microsoft YaHei / Noto Sans SC.
- See `src/styles/typography.css` and `docs/brand/design-system/DESIGN.md` §5.

See the header comment in `src/styles/docs-tokens.css` for the full naming map.

### Information architecture

| Section | Purpose |
| --- | --- |
| Getting Started | Install + quickstart |
| Core Concepts | Runtime, policy, audit |
| Runtime API | Embed surfaces + JSON-RPC |
| Agents / Tools / Memory | Product pillars |
| Workflows | DAG composition + tutorial |
| Observability | Traces & audit |
| Deployment | Local, workers, FAQ |

Locales: **English (default, root)** and **简体中文 (`/zh-cn/`)**. Language switcher is built into Starlight’s header.

## Build output

```bash
pnpm build
```

Produces pure static files under **`dist/`**:

- HTML pages for every locale/route
- Pagefind search index (`dist/pagefind/`)
- Hashed assets under `dist/_astro/`
- Fonts / logos from `public/`

Suitable for:

| Target | Notes |
| --- | --- |
| **Vercel** | `vercel.json` included; root directory = `apps/docs` |
| **Cloudflare Pages** | Build `pnpm build`, output `dist` |
| **阿里云 OSS + CDN** | Sync `dist/` to bucket; set `index.html` + SPA-less static hosting (directory URLs) |

### Vercel project settings

- **Root Directory**: `apps/docs`
- **Install**: `pnpm install`
- **Build**: `pnpm build`
- **Output**: `dist`

### 阿里云 OSS 备注

构建产物为纯静态，可直接：

```bash
# 示例：使用 ossutil 同步（需自行配置凭据）
ossutil cp -r dist/ oss://your-bucket/docs/ --update
```

国内访问建议绑定 CDN，并为 `/fonts/*`、`/_astro/*` 设置长缓存（见 `vercel.json` headers 参考）。

## Search

Starlight’s built-in **Pagefind** search is enabled. It indexes at build time and supports keyboard navigation (`/` to focus, arrows, Enter, Esc). Dev server shows a notice that search needs a production build — use `pnpm build && pnpm preview` to verify.

## Custom components

| Component | Path | Role |
| --- | --- | --- |
| `CategoryTag` | `src/components/ui/CategoryTag.astro` | Capability taxonomy badge |
| `Callout` | `src/components/ui/Callout.astro` | Note / Tip / Warning / Danger |
| `CodeBlock` | `src/components/ui/CodeBlock.astro` | Copy + lang label + optional line numbers |
| `VersionSelector` | `src/components/ui/VersionSelector.astro` | Placeholder version picker |
| `ApiReference` | `src/components/layouts/ApiReference.astro` | 3-column API template |
| `Steps` / `Step` | `src/components/mdx/` | Numbered procedure lists |
| `Tabs` / `Tab` | `src/components/mdx/` | Client tab panels |
| `Accordion` / `AccordionItem` | `src/components/mdx/` | FAQ-style disclosure |
| `Cards` / `Card` | `src/components/mdx/` | Link/content card grids |
| `Breadcrumb` | `src/components/Breadcrumb.astro` | Path crumbs under page title |
| `PageFeedback` | `src/components/PageFeedback.astro` | “Was this helpful?” (sessionStorage) |
| Landing set | `src/components/landing/*` | Hero, features, terminal mock, quickstart |

Import MDX components at the top of a page, for example:

```mdx
import Steps from '../../../components/mdx/Steps.astro';
import Step from '../../../components/mdx/Step.astro';
```

## Performance checklist

- Self-hosted WOFF2 fonts with `font-display: swap` + preload
- Static output only (no SSR runtime)
- Image pipeline via `sharp`
- CSS variables (no large runtime theme JS beyond forced dark)

Target: Lighthouse Performance **90+** on production deploy.

## Brand sync policy

Do **not** invent a second token set inside this package.

```bash
# After editing docs/brand/design-system/*
pnpm sync-brand
```

Then run `make brand-check` from the repo root when brand consumers change.
