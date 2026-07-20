#!/usr/bin/env node
/**
 * Generate Mintlify-style AI-readable surfaces:
 *   - public/llms.txt
 *   - public/llms-full.txt (titles + descriptions + paths)
 *   - public/skill.md
 *
 * Run after content changes: pnpm generate-ai-surface
 * Also invoked from prebuild when present.
 */
import { readdirSync, readFileSync, writeFileSync, statSync, mkdirSync } from 'node:fs';
import { join, relative, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const docsRoot = join(__dirname, '..');
const contentRoot = join(docsRoot, 'src/content/docs');
const publicDir = join(docsRoot, 'public');

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    if (name.startsWith('.')) continue;
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) walk(p, out);
    else if (name.endsWith('.mdx') || name.endsWith('.md')) out.push(p);
  }
  return out;
}

function parseFrontmatter(raw) {
  if (!raw.startsWith('---')) return { data: {}, body: raw };
  const end = raw.indexOf('\n---', 3);
  if (end === -1) return { data: {}, body: raw };
  const fm = raw.slice(3, end).trim();
  const body = raw.slice(end + 4);
  const data = {};
  for (const line of fm.split('\n')) {
    const m = line.match(/^([A-Za-z0-9_-]+):\s*(.*)$/);
    if (!m) continue;
    let v = m[2].trim();
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) {
      v = v.slice(1, -1);
    }
    data[m[1]] = v;
  }
  return { data, body };
}

function fileToUrl(file) {
  let rel = relative(contentRoot, file).replace(/\\/g, '/');
  rel = rel.replace(/\.mdx?$/, '');
  if (rel.endsWith('/index')) rel = rel.slice(0, -'/index'.length);
  if (rel === 'index' || rel === '404') return rel === 'index' ? '/' : null;
  // zh-cn pages
  if (rel.startsWith('zh-cn/')) {
    const rest = rel.slice('zh-cn/'.length);
    if (rest === 'index') return '/zh-cn/';
    return `/zh-cn/${rest}/`;
  }
  return `/${rel}/`;
}

const files = walk(contentRoot);
const entries = [];
for (const file of files) {
  const raw = readFileSync(file, 'utf8');
  const { data } = parseFrontmatter(raw);
  const url = fileToUrl(file);
  if (!url || data.template === 'splash' && url === '/') {
    // include landing with special title
  }
  if (!url) continue;
  if (url.includes('/404')) continue;
  entries.push({
    url,
    title: data.title || url,
    description: data.description || '',
    locale: url.startsWith('/zh-cn') ? 'zh-CN' : 'en',
  });
}

entries.sort((a, b) => a.url.localeCompare(b.url));

const site = 'https://carina.nebutra.com';
const en = entries.filter((e) => e.locale === 'en');
const zh = entries.filter((e) => e.locale === 'zh-CN');

const llms = [
  '# Carina Documentation',
  '',
  `> Local-first AI agent runtime with policy, hash-chained audit, and transactional rollback.`,
  '',
  `Site: ${site}`,
  '',
  '## English',
  '',
  ...en.map((e) => `- [${e.title}](${site}${e.url}): ${e.description || e.title}`),
  '',
  '## 简体中文',
  '',
  ...zh.map((e) => `- [${e.title}](${site}${e.url}): ${e.description || e.title}`),
  '',
  '## Machine-readable',
  '',
  `- Full index: ${site}/llms-full.txt`,
  `- Skill: ${site}/skill.md`,
  `- Markdown export: append \`index.md\` to any docs path (e.g. ${site}/getting-started/quickstart/index.md)`,
  `- JSON-RPC catalogs: ${site}/data/rpc-catalog-0.6.x.json · ${site}/data/rpc-catalog-next.json`,
  '',
].join('\n');

const llmsFull = [
  '# Carina Documentation — full index',
  '',
  ...entries.map((e) => {
    return [`## ${e.title}`, '', e.description, '', `- URL: ${site}${e.url}`, `- Markdown: ${site}${e.url}index.md`, ''].join('\n');
  }),
].join('\n');

const skill = `---
name: carina-docs
description: Use Carina documentation to answer questions about the local-first AI agent runtime (policy kernel, audit, sessions, JSON-RPC, workflows).
---

# Carina docs skill

## When to use
- Installing or running the Carina CLI / daemon
- Policy profiles, capabilities, approvals
- Sessions, tasks, audit, rollback
- JSON-RPC methods and gateway usage
- Workflows, tools, MCP, workers

## Sources
- Human docs: ${site}
- LLM index: ${site}/llms.txt
- Full index: ${site}/llms-full.txt
- Method catalog (stable): ${site}/data/rpc-catalog-0.6.x.json
- Method catalog (next): ${site}/data/rpc-catalog-next.json

## Preferred workflow
1. Read llms.txt for the page map.
2. Fetch the relevant page as Markdown (\`…/path/index.md\`).
3. For RPC details, consult the method catalog JSON.
4. Cite the docs URL in answers.

## Install
\`\`\`bash
# Cursor / skill runners (example)
npx skills add ${site}
\`\`\`
`;

mkdirSync(publicDir, { recursive: true });
writeFileSync(join(publicDir, 'llms.txt'), llms);
writeFileSync(join(publicDir, 'llms-full.txt'), llmsFull);
writeFileSync(join(publicDir, 'skill.md'), skill);
console.log(`✓ llms.txt (${en.length} en · ${zh.length} zh-CN)`);
console.log(`✓ llms-full.txt`);
console.log(`✓ skill.md`);
