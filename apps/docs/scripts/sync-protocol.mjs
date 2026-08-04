#!/usr/bin/env node
/**
 * Sync JSON-RPC method registry into the docs package for static builds.
 *
 * Source (monorepo): protocol/jsonrpc/methods.json
 * Outputs:
 *   src/data/rpc-catalog.json          — default SSG import (next/head)
 *   src/data/rpc-catalog-next.json
 *   src/data/rpc-catalog-0.8.x.json    — stable channel pin (current release line)
 *   public/data/rpc-catalog-*.json     — client-fetchable trees
 *   src/data/versions.json + public
 *
 * Run: pnpm sync-protocol
 *
 * Channel model (objective):
 *   - stable `0.8.x` — docs surface for the current 0.8 release line
 *   - `next` — monorepo head of methods.json (may include unreleased methods)
 * Both are regenerated from the same source on sync; release freezes happen by
 * committing the generated JSON on the release branch.
 */
import { readFileSync, writeFileSync, mkdirSync, existsSync, copyFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const docsRoot = resolve(__dirname, '..');
const repoRoot = resolve(docsRoot, '../..');
const source = join(repoRoot, 'protocol/jsonrpc/methods.json');
const outDir = join(docsRoot, 'src/data');
const publicData = join(docsRoot, 'public/data');
const outFile = join(outDir, 'rpc-catalog.json');

/** Stable docs channel id — keep in lock-step with the current published major.minor line. */
const STABLE_CHANNEL = '0.8.x';

if (!existsSync(source)) {
  console.error(`Missing protocol registry: ${source}`);
  process.exit(1);
}

const raw = JSON.parse(readFileSync(source, 'utf8'));
const apis = raw.apis ?? {};

const groups = Object.entries(apis).map(([id, methods]) => ({
  id,
  label: id
    .split('_')
    .map((s) => s.charAt(0).toUpperCase() + s.slice(1))
    .join(' '),
  methods: (Array.isArray(methods) ? methods : []).map((m) => ({
    method: m.method,
    scope: m.scope ?? 'read',
    remote: Boolean(m.remote),
    control_plane_write: Boolean(m.control_plane_write),
    params: m.params ?? {},
    result: m.result ?? 'object',
  })),
}));

const base = {
  generated_at: new Date().toISOString(),
  protocol_version: raw.version ?? 'unknown',
  source: 'protocol/jsonrpc/methods.json',
  method_count: groups.reduce((n, g) => n + g.methods.length, 0),
  groups,
};

const nextCatalog = {
  ...base,
  channel: 'next',
  badge: 'preview',
  note: 'Tracks protocol/jsonrpc/methods.json from the monorepo head.',
};

const stableCatalog = {
  ...base,
  channel: STABLE_CHANNEL,
  badge: 'stable',
  note: `Docs channel for the ${STABLE_CHANNEL} release line. Re-sync on release branches freezes the pin.`,
};

mkdirSync(outDir, { recursive: true });
mkdirSync(publicData, { recursive: true });

function writeJson(path, data) {
  writeFileSync(path, JSON.stringify(data, null, 2) + '\n');
  console.log(`✓ ${path}`);
}

const stableFile = `rpc-catalog-${STABLE_CHANNEL}.json`;

// Default SSG import = next (latest protocol surface for docs development)
writeJson(outFile, nextCatalog);
writeJson(join(outDir, 'rpc-catalog-next.json'), nextCatalog);
writeJson(join(outDir, stableFile), stableCatalog);
writeJson(join(publicData, 'rpc-catalog-next.json'), nextCatalog);
writeJson(join(publicData, stableFile), stableCatalog);

// versions manifest for clients
const versionsPath = join(outDir, 'versions.json');
const versions = {
  default: STABLE_CHANNEL,
  versions: [
    {
      id: STABLE_CHANNEL,
      label: STABLE_CHANNEL,
      description: `Stable docs surface for the ${STABLE_CHANNEL} release line.`,
      badge: 'stable',
      catalog: `/data/${stableFile}`,
    },
    {
      id: 'next',
      label: 'next',
      description: 'Tracks the in-repo protocol registry (latest methods.json).',
      badge: 'preview',
      catalog: '/data/rpc-catalog-next.json',
    },
  ],
};
writeJson(versionsPath, versions);
copyFileSync(versionsPath, join(publicData, 'versions.json'));
console.log(
  `  protocol ${base.protocol_version} · ${base.method_count} methods · ${groups.length} groups · channels ${STABLE_CHANNEL} + next`,
);
