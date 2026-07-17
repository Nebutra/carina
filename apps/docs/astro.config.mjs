// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import tailwindcss from '@tailwindcss/vite';
import remarkMath from 'remark-math';
import rehypeKatex from 'rehype-katex';
import { carinaDark } from './src/themes/carina-code.mjs';

/**
 * Carina docs site — Astro + Starlight
 *
 * Theme architecture:
 *  1. Brand tokens live in src/styles/brand/ (synced from docs/brand/design-system/)
 *  2. starlight-theme.css maps Starlight --sl-* vars onto Carina tokens
 *  3. global.css wires Tailwind v4 + cascade layers
 *
 * Dual theme: brand semantics switch on data-theme (light/dark); Starlight's
 * built-in ThemeProvider/ThemeSelect handle persistence + FOUC guard.
 *
 * Math: remark-math + rehype-katex (inline $…$ / display $$…$$).
 */
export default defineConfig({
  site: 'https://carina.nebutra.com',
  trailingSlash: 'always',
  markdown: {
    // Still supported in Astro 7 (processor API preferred long-term).
    remarkPlugins: [remarkMath],
    rehypePlugins: [
      [
        rehypeKatex,
        {
          // Prefer render over hard-fail for authoring DX
          throwOnError: false,
          strict: 'ignore',
          // Allow more AMS / HTML-ish constructs used in complex formulas
          trust: true,
          output: 'htmlAndMathml',
        },
      ],
    ],
  },
  integrations: [
    starlight({
      title: 'Carina',
      description:
        'Local-first AI agent runtime with policy, hash-chained audit, and transactional rollback.',
      components: {
        Head: './src/components/Head.astro',
        // Default header row + Mintlify-style section tabs strip
        Header: './src/components/Header.astro',
        // Splash/landing: hide default title (Hero owns hierarchy)
        // Doc pages: breadcrumb above title
        PageTitle: './src/components/PageTitle.astro',
        // Feedback strip above meta + pagination
        Footer: './src/components/Footer.astro',
        // Prev/Next with denser Mintlify-style cards
        Pagination: './src/components/Pagination.astro',
      },
      // Show "Last updated" from git history when available
      lastUpdated: true,
      logo: {
        // Wordmark ink is baked into the SVG root color; ship one per theme.
        light: './src/assets/carina-horizontal-brand.svg',
        dark: './src/assets/carina-horizontal-brand-dark.svg',
        alt: 'Carina',
        replacesTitle: true,
      },
      favicon: '/favicon.svg',
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/nebutra/carina',
        },
      ],
      // Default English at root; Simplified Chinese under /zh-cn/
      defaultLocale: 'root',
      locales: {
        root: {
          label: 'English',
          lang: 'en',
        },
        'zh-cn': {
          label: '简体中文',
          lang: 'zh-CN',
        },
      },
      customCss: [
        // Order: faces → brand tokens → docs API → Tailwind → Starlight map → type roles → UX
        // starlight-theme.css must load AFTER global.css (Tailwind) so its
        // unlayered --sl-* map outranks starlight-tailwind's default palette.
        './src/styles/fonts.css',
        // Newsreader Variable — docs H1 / hero display (self-hosted via fontsource)
        '@fontsource-variable/newsreader/wght.css',
        './src/styles/brand/variables.css',
        './src/styles/docs-tokens.css',
        './src/styles/global.css',
        './src/styles/starlight-theme.css',
        './src/styles/typography.css',
        './src/styles/ux.css',
        // Modular SaaS polish (loads last; overrides for Mintlify-grade UX)
        './src/styles/polish/_tokens.css',
        './src/styles/polish/code.css',
        './src/styles/polish/nav.css',
        './src/styles/polish/content.css',
        './src/styles/polish/search.css',
        './src/styles/polish/landing.css',
        './src/styles/polish/motion.css',
        // KaTeX (math) — after polish so docs-math.css can override colors
        'katex/dist/katex.min.css',
        './src/styles/polish/math.css',
      ],
      // Built-in Pagefind search (keyboard: /, Esc)
      pagefind: true,
      /*
       * Expressive Code — aligned with Mintlify / Fumadocs code-block practice:
       *  - always-dark mineral surface (readable on light pages)
       *  - code frames (not macOS terminal chrome)
       *  - filename in header via title="…" meta; short CLI uses frame="none"
       *  - copy always present; no drop shadows
       *  - Carina Shiki theme (six-color capability map)
       * @see https://www.mintlify.com/docs/create/code
       * @see https://www.fumadocs.dev/docs/ui/components/codeblock
       */
      expressiveCode: {
        themes: [carinaDark],
        defaultProps: {
          frame: 'code',
          wrap: false,
        },
        frames: {
          extractFileNameFromCode: true,
          showCopyToClipboardButton: true,
          removeCommentsWhenCopyingTerminalFrames: true,
        },
        styleOverrides: {
          borderRadius: '10px',
          borderWidth: '1px',
          // CSS vars resolve at paint time; only Shiki theme keeps concrete hex.
          borderColor: 'var(--polish-code-border, var(--carina-code-border))',
          codeFontFamily: 'var(--docs-font-mono)',
          uiFontFamily: 'var(--docs-font-sans)',
          codeFontSize: '13px',
          codeLineHeight: '1.7',
          codePaddingBlock: '1rem',
          codePaddingInline: '1.15rem',
          codeBackground: 'var(--polish-code-bg, var(--carina-code-void))',
          uiPaddingBlock: '0.55rem',
          uiPaddingInline: '0.85rem',
          uiFontSize: '12px',
          frames: {
            shadowColor: 'transparent',
            frameBoxShadowCssValue: 'none',
            editorBackground: 'var(--polish-code-bg, var(--carina-code-void))',
            terminalBackground: 'var(--polish-code-bg, var(--carina-code-void))',
            editorTabBarBackground: 'var(--polish-code-chrome, var(--carina-code-chrome))',
            editorActiveTabBackground: 'var(--polish-code-bg, var(--carina-code-void))',
            editorActiveTabForeground: 'var(--polish-code-muted, var(--carina-code-muted))',
            editorActiveTabBorderColor: 'transparent',
            editorActiveTabIndicatorTopColor: 'transparent',
            editorActiveTabIndicatorBottomColor: 'transparent',
            editorActiveTabIndicatorHeight: '0px',
            editorTabBarBorderBottomColor: 'var(--polish-code-border, var(--carina-code-border))',
            editorTabBarBorderColor: 'transparent',
            editorTabsMarginInlineStart: '0',
            editorTabsMarginBlockStart: '0',
            editorTabBorderRadius: '0',
            terminalTitlebarBackground: 'var(--polish-code-chrome, var(--carina-code-chrome))',
            terminalTitlebarBorderBottomColor: 'var(--polish-code-border, var(--carina-code-border))',
            terminalTitlebarForeground: 'var(--polish-code-muted, var(--carina-code-muted))',
            terminalTitlebarDotsOpacity: '0',
            inlineButtonBorder: 'transparent',
            inlineButtonBorderOpacity: '0',
            inlineButtonBackground: 'var(--polish-code-chrome, var(--carina-code-chrome))',
            inlineButtonBackgroundIdleOpacity: '0',
            inlineButtonBackgroundHoverOrFocusOpacity: '1',
            inlineButtonBackgroundActiveOpacity: '1',
            inlineButtonForeground: 'var(--polish-code-muted, var(--carina-code-muted))',
            tooltipSuccessBackground: 'var(--polish-code-success-bg)',
            tooltipSuccessForeground: 'var(--polish-code-success-fg)',
          },
        },
      },
      sidebar: [
        {
          label: 'Getting Started',
          translations: { 'zh-CN': '快速开始' },
          items: [
            {
              label: 'Introduction',
              translations: { 'zh-CN': '简介' },
              link: '/getting-started/introduction/',
            },
            {
              label: 'Installation',
              translations: { 'zh-CN': '安装' },
              link: '/getting-started/installation/',
            },
            {
              label: 'Quickstart',
              translations: { 'zh-CN': '快速上手' },
              link: '/getting-started/quickstart/',
            },
            {
              label: 'Math notation',
              translations: { 'zh-CN': '数学公式' },
              link: '/getting-started/math/',
            },
          ],
        },
        {
          label: 'Core Concepts',
          translations: { 'zh-CN': '核心概念' },
          items: [
            {
              label: 'Runtime model',
              translations: { 'zh-CN': '运行时模型' },
              link: '/concepts/runtime/',
            },
            {
              label: 'Policy & capabilities',
              translations: { 'zh-CN': '策略与能力' },
              link: '/concepts/policy/',
            },
            {
              label: 'Audit & rollback',
              translations: { 'zh-CN': '审计与回滚' },
              link: '/concepts/audit/',
            },
          ],
        },
        {
          label: 'Runtime API',
          translations: { 'zh-CN': 'Runtime API' },
          items: [
            {
              label: 'Overview',
              translations: { 'zh-CN': '概览' },
              link: '/api/overview/',
            },
            {
              label: 'Sessions',
              translations: { 'zh-CN': '会话' },
              link: '/api/sessions/',
            },
            {
              label: 'Method catalog',
              translations: { 'zh-CN': '方法目录' },
              link: '/api/methods/',
            },
            {
              label: 'JSON-RPC reference',
              translations: { 'zh-CN': 'JSON-RPC 参考' },
              link: '/api/json-rpc/',
            },
            {
              label: 'API versions',
              translations: { 'zh-CN': 'API 版本通道' },
              link: '/api/versions/',
            },
          ],
        },
        {
          label: 'Agents',
          translations: { 'zh-CN': 'Agents' },
          items: [
            {
              label: 'Overview',
              translations: { 'zh-CN': '概览' },
              link: '/agents/overview/',
            },
            {
              label: 'Sub-agents',
              translations: { 'zh-CN': '子 Agent' },
              link: '/agents/sub-agents/',
            },
          ],
        },
        {
          label: 'Tools',
          translations: { 'zh-CN': 'Tools' },
          items: [
            {
              label: 'Overview',
              translations: { 'zh-CN': '概览' },
              link: '/tools/overview/',
            },
            {
              label: 'MCP integrations',
              translations: { 'zh-CN': 'MCP 集成' },
              link: '/tools/mcp/',
            },
          ],
        },
        {
          label: 'Memory',
          translations: { 'zh-CN': 'Memory' },
          items: [
            {
              label: 'Overview',
              translations: { 'zh-CN': '概览' },
              link: '/memory/overview/',
            },
          ],
        },
        {
          label: 'Workflows',
          translations: { 'zh-CN': 'Workflows' },
          items: [
            {
              label: 'Overview',
              translations: { 'zh-CN': '概览' },
              link: '/workflows/overview/',
            },
            {
              label: 'Tutorial: review pipeline',
              translations: { 'zh-CN': '教程：审查流水线' },
              link: '/workflows/tutorial-review/',
            },
          ],
        },
        {
          label: 'Observability',
          translations: { 'zh-CN': '可观测性' },
          items: [
            {
              label: 'Traces & audit',
              translations: { 'zh-CN': '追踪与审计' },
              link: '/observability/traces/',
            },
          ],
        },
        {
          label: 'Deployment',
          translations: { 'zh-CN': '部署' },
          items: [
            {
              label: 'Local install',
              translations: { 'zh-CN': '本地安装' },
              link: '/deployment/local/',
            },
            {
              label: 'Workers',
              translations: { 'zh-CN': 'Workers' },
              link: '/deployment/workers/',
            },
            {
              label: 'FAQ',
              translations: { 'zh-CN': '常见问题' },
              link: '/deployment/faq/',
            },
          ],
        },
      ],
      head: [
        {
          tag: 'link',
          attrs: {
            rel: 'preload',
            href: '/fonts/geist-sans-latin-variable.woff2',
            as: 'font',
            type: 'font/woff2',
            crossorigin: 'anonymous',
          },
        },
        {
          tag: 'link',
          attrs: {
            rel: 'preload',
            href: '/fonts/geist-mono-latin-variable.woff2',
            as: 'font',
            type: 'font/woff2',
            crossorigin: 'anonymous',
          },
        },
        {
          tag: 'meta',
          attrs: {
            name: 'theme-color',
            content: '#0d1214',
          },
        },
      ],
    }),
  ],
  vite: {
    plugins: [tailwindcss()],
    // Allow resolving brand assets from the monorepo when developing locally.
    server: {
      fs: {
        allow: ['../..'],
      },
    },
  },
  image: {
    // sharp is already a dependency; Astro uses it for optimized static images.
    service: {
      entrypoint: 'astro/assets/services/sharp',
    },
  },
  build: {
    // Pure static output for Vercel / Cloudflare Pages / OSS + CDN.
    format: 'directory',
    inlineStylesheets: 'auto',
  },
});
