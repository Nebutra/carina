#!/usr/bin/env node
/**
 * Sync JSON-RPC method registry into the docs package for static builds.
 *
 * Source (monorepo): protocol/jsonrpc/methods.json
 * Output:           src/data/rpc-catalog.json
 *
 * Run: pnpm sync-protocol
 */
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const docsRoot = resolve(__dirname, '..');
const repoRoot = resolve(docsRoot, '../..');
const source = join(repoRoot, 'protocol/jsonrpc/methods.json');
const outDir = join(docsRoot, 'src/data');
const outFile = join(outDir, 'rpc-catalog.json');

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

const catalog = {
  generated_at: new Date().toISOString(),
  protocol_version: raw.version ?? 'unknown',
  source: 'protocol/jsonrpc/methods.json',
  method_count: groups.reduce((n, g) => n + g.methods.length, 0),
  groups,
};

mkdirSync(outDir, { recursive: true });
writeFileSync(outFile, JSON.stringify(catalog, null, 2) + '\n');
console.log(
  `✓ ${outFile}\n  protocol ${catalog.protocol_version} · ${catalog.method_count} methods · ${groups.length} groups`,
);
