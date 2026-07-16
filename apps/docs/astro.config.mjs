// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import tailwindcss from '@tailwindcss/vite';

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
 */
export default defineConfig({
  site: 'https://docs.carina.dev',
  trailingSlash: 'always',
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
        PageTitle: './src/components/PageTitle.astro',
      },
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
        './src/styles/brand/variables.css',
        './src/styles/docs-tokens.css',
        './src/styles/global.css',
        './src/styles/starlight-theme.css',
        './src/styles/typography.css',
        './src/styles/ux.css',
      ],
      // Built-in Pagefind search (keyboard: /, Esc)
      pagefind: true,
      // Expressive Code: copy button, language label, line numbers via CSS overrides
      // Code blocks stay dark in BOTH themes (Mintlify signature look);
      // frame chrome is pinned to dark primitives in ux.css.
      expressiveCode: {
        themes: ['github-dark-default'],
        // Always-dark code (Mintlify signature) in both site themes.
        themeCssSelector: () => ':root',
        useDarkModeMediaQuery: false,
        styleOverrides: {
          borderRadius: 'var(--radius-lg)',
          borderWidth: '1px',
          codeFontFamily: 'var(--docs-font-mono)',
          uiFontFamily: 'var(--docs-font-sans)',
          frames: {
            shadowColor: 'transparent',
            editorBackground: 'var(--carina-surface)',
            terminalBackground: 'var(--carina-surface)',
            editorTabBarBackground: 'var(--carina-surface-raised)',
            editorActiveTabBackground: 'var(--carina-surface)',
            terminalTitlebarBackground: 'var(--carina-surface-raised)',
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
              label: 'JSON-RPC reference',
              translations: { 'zh-CN': 'JSON-RPC 参考' },
              link: '/api/json-rpc/',
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
