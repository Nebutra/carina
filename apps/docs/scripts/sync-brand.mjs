#!/usr/bin/env node
/**
 * Sync brand design tokens + fonts from the monorepo authority path
 * into this docs package (deployable, no monorepo path dependency).
 *
 * Authority: docs/brand/ (see docs/brand/AGENTS.md)
 * Run: pnpm sync-brand
 */
import { copyFileSync, mkdirSync, existsSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const docsRoot = resolve(__dirname, '..');
const repoRoot = resolve(docsRoot, '../..');
const brandRoot = join(repoRoot, 'docs/brand');

const files = [
  {
    from: join(brandRoot, 'design-system/variables.css'),
    to: join(docsRoot, 'src/styles/brand/variables.css'),
    banner: `/**
 * SOURCE OF TRUTH (authoritative): docs/brand/design-system/variables.css
 * Re-synced into this package by: pnpm sync-brand
 * Do not invent local tokens here — edit the brand design system, then sync.
 */
`,
  },
  {
    from: join(brandRoot, 'design-system/tailwind-v4.css'),
    to: join(docsRoot, 'src/styles/brand/tailwind-v4.css'),
    banner: `/**
 * SOURCE: docs/brand/design-system/tailwind-v4.css
 * Synced by: pnpm sync-brand
 */
`,
  },
];

const fonts = [
  'geist-sans-latin-variable.woff2',
  'geist-mono-latin-variable.woff2',
  'CarinaDisplayAlpha-Regular.woff2',
];

const logos = [
  'carina-symbol.svg',
  'carina-horizontal-brand.svg',
  'carina-wordmark.svg',
  'carina-symbol-high-contrast.svg',
];

function ensureDir(path) {
  mkdirSync(path, { recursive: true });
}

function syncCss({ from, to, banner }) {
  if (!existsSync(from)) {
    console.error(`Missing source: ${from}`);
    process.exit(1);
  }
  ensureDir(dirname(to));
  const body = readFileSync(from, 'utf8');
  // Strip a previous banner if re-syncing
  const stripped = body.replace(/^\/\*\*[\s\S]*?\*\/\s*/, '');
  writeFileSync(to, banner + stripped);
  console.log(`✓ ${to}`);
}

function copy(from, to) {
  if (!existsSync(from)) {
    console.error(`Missing source: ${from}`);
    process.exit(1);
  }
  ensureDir(dirname(to));
  copyFileSync(from, to);
  console.log(`✓ ${to}`);
}

console.log('Syncing Carina brand assets into apps/docs …\n');

for (const file of files) syncCss(file);

const fontDir = join(docsRoot, 'public/fonts');
ensureDir(fontDir);
for (const font of fonts) {
  copy(join(brandRoot, 'assets/fonts', font), join(fontDir, font));
}

for (const logo of logos) {
  copy(join(brandRoot, 'assets/logo', logo), join(docsRoot, 'public', logo));
}
copy(join(brandRoot, 'assets/logo/carina-symbol.svg'), join(docsRoot, 'public/favicon.svg'));

console.log('\nDone. Brand tokens + fonts are ready for static deploy.');
