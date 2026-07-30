#!/usr/bin/env node
/**
 * Keep public/install.sh identical to repo scripts/install.sh (homepage + install docs).
 */
import { copyFileSync, chmodSync, existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const src = join(root, '../../scripts/install.sh');
const dest = join(root, 'public/install.sh');
if (!existsSync(src)) {
  console.error(`sync-install: missing ${src}`);
  process.exit(1);
}
copyFileSync(src, dest);
chmodSync(dest, 0o755);
console.log('sync-install: public/install.sh ← scripts/install.sh');
