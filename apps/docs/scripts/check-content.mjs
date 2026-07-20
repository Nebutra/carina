import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import remarkMdx from 'remark-mdx';
import remarkParse from 'remark-parse';
import { unified } from 'unified';
import { visit } from 'unist-util-visit';
import { sectionEntries } from '../src/config/section-entries.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const docsRoot = join(root, 'src/content/docs');
const publicRoot = join(root, 'public');
const parser = unified().use(remarkParse).use(remarkMdx);
const markdownParser = unified().use(remarkParse);
const failures = [];
const trees = new Map();

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? walk(path) : [path];
  });
}

function routeFor(file) {
  let id = relative(docsRoot, file).split(sep).join('/').replace(/\.mdx?$/, '');
  if (id === 'index') return '/';
  id = id.replace(/\/index$/, '');
  return `/${id}/`;
}

function stripFrontmatter(raw) {
  return raw.replace(/^---\n[\s\S]*?\n---\n/, '');
}

function proseSize(raw) {
  return stripFrontmatter(raw)
    .replace(/^\s*import\s+.*$/gm, '')
    .replace(/```[\s\S]*?```/g, '')
    .replace(/<[^>]+>/g, '')
    .replace(/[\s\p{P}\p{S}_]+/gu, '').length;
}

function headingCount(tree) {
  let count = 0;
  visit(tree, 'heading', () => count++);
  return count;
}

function linkValues(tree, raw) {
  const links = [];
  visit(tree, (node) => {
    if ((node.type === 'link' || node.type === 'image') && typeof node.url === 'string') {
      links.push(node.url);
    }
    if (node.type !== 'mdxJsxFlowElement' && node.type !== 'mdxJsxTextElement') return;
    for (const attr of node.attributes || []) {
      if ((attr.name === 'href' || attr.name === 'src') && typeof attr.value === 'string') {
        links.push(attr.value);
      }
    }
  });
  for (const match of raw.matchAll(/\b(?:href|src)=["']([^"']+)["']/g)) {
    links.push(match[1]);
  }
  return links;
}

function normalizeRoute(pathname) {
  if (pathname === '/') return '/';
  return `${pathname.replace(/\/+$/, '')}/`;
}

const files = walk(docsRoot).filter((file) => /\.mdx?$/.test(file));
const byRoute = new Map(files.map((file) => [routeFor(file), file]));

for (const entry of Object.values(sectionEntries)) {
  for (const route of [`/${entry}/`, `/zh-cn/${entry}/`]) {
    if (!byRoute.has(route)) failures.push(`section entry is missing: ${route}`);
  }
}

for (const file of files) {
  const route = routeFor(file);
  const raw = readFileSync(file, 'utf8');
  let tree;
  try {
    tree = parser.parse(raw);
  } catch {
    tree = markdownParser.parse(raw);
  }
  trees.set(file, tree);

  for (const value of new Set(linkValues(tree, raw))) {
    if (!value || value.startsWith('#') || /^(mailto:|tel:|data:)/.test(value)) continue;
    let url;
    try {
      url = new URL(value, `https://carina.nebutra.com${route}`);
    } catch {
      failures.push(`${route}: invalid link ${JSON.stringify(value)}`);
      continue;
    }
    if (url.origin !== 'https://carina.nebutra.com') continue;
    if (url.pathname.endsWith('/index.md')) continue;

    const targetRoute = normalizeRoute(decodeURIComponent(url.pathname));
    const publicFile = join(publicRoot, decodeURIComponent(url.pathname).replace(/^\//, ''));
    if (!byRoute.has(targetRoute) && !existsSync(publicFile)) {
      failures.push(`${route}: missing internal target ${url.pathname}`);
      continue;
    }

    if (route.startsWith('/zh-cn/') && byRoute.has(targetRoute) && !targetRoute.startsWith('/zh-cn/')) {
      failures.push(`${route}: Chinese page links to English fallback ${url.pathname}`);
    }
  }
}

for (const file of files) {
  const route = routeFor(file);
  if (route.startsWith('/zh-cn/') || route === '/404/') continue;
  const relativePath = relative(docsRoot, file);
  const zhFile = join(docsRoot, 'zh-cn', relativePath);
  if (!existsSync(zhFile)) {
    failures.push(`${route}: missing zh-cn page pair`);
    continue;
  }

  if (route === '/api/methods/') continue;
  const enRaw = readFileSync(file, 'utf8');
  const zhRaw = readFileSync(zhFile, 'utf8');
  const enSize = proseSize(enRaw);
  const zhSize = proseSize(zhRaw);
  const enHeadings = headingCount(trees.get(file) || markdownParser.parse(enRaw));
  const zhHeadings = headingCount(trees.get(zhFile) || markdownParser.parse(zhRaw));

  if (enSize >= 500 && zhSize / enSize < 0.24) {
    failures.push(
      `${route}: zh-cn prose coverage ${(zhSize / enSize * 100).toFixed(1)}% is below 24%`,
    );
  }
  if (enHeadings >= 4 && zhHeadings / enHeadings < 0.35) {
    failures.push(
      `${route}: zh-cn heading coverage ${zhHeadings}/${enHeadings} is below 35%`,
    );
  }
  if (/详见英文|完整内容见英文|See the English/i.test(zhRaw)) {
    failures.push(`${route}: zh-cn page contains an English fallback placeholder`);
  }
}

if (failures.length > 0) {
  console.error(`docs content check failed (${failures.length}):`);
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log(`docs content check passed: ${files.length} MDX files, internal links and locale pairs valid`);
