/**
 * Mintlify-style Markdown export routes:
 *   /getting-started/quickstart/index.md
 *   /zh-cn/api/methods/index.md
 */
import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

export const prerender = true;

function stripFrontmatter(raw: string): { title?: string; description?: string; body: string } {
  if (!raw.startsWith('---')) return { body: raw };
  const end = raw.indexOf('\n---', 3);
  if (end === -1) return { body: raw };
  const fm = raw.slice(3, end);
  const body = raw.slice(end + 4);
  const title = fm.match(/^title:\s*(.+)$/m)?.[1]?.replace(/^["']|["']$/g, '');
  const description = fm.match(/^description:\s*(.+)$/m)?.[1]?.replace(/^["']|["']$/g, '');
  return { title, description, body };
}

function mdxToPlain(body: string): string {
  return body
    .split('\n')
    .filter((line) => !/^\s*import\s+/.test(line))
    .join('\n')
    .replace(/^\s*<[A-Z][\w.]*[\s\S]*?\/>\s*$/gm, '')
    .replace(/^\s*<\/?[A-Z][\w.]*[^>]*>\s*$/gm, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

function idToFs(id: string): string[] {
  const base = id.replace(/\.mdx?$/, '').replace(/\/index$/, '');
  const root = join(process.cwd(), 'src/content/docs');
  return [
    join(root, `${base}.mdx`),
    join(root, `${base}.md`),
    join(root, base, 'index.mdx'),
    join(root, base, 'index.md'),
  ];
}

export async function getStaticPaths() {
  const docs = await getCollection('docs');
  return docs
    .filter((e) => !String(e.id).includes('404'))
    .map((entry) => {
      const id = String(entry.id)
        .replace(/\.mdx?$/, '')
        .replace(/\/index$/, '');
      const slug = id === 'index' ? 'index' : `${id}/index`;
      return { params: { slug }, props: { id } };
    });
}

export const GET: APIRoute = async ({ props, site }) => {
  const id = String(props.id || 'index');
  const candidates = idToFs(id);
  const file = candidates.find((p) => existsSync(p));

  let title = id;
  let description: string | undefined;
  let body = '';

  if (file) {
    const raw = readFileSync(file, 'utf8');
    const parsed = stripFrontmatter(raw);
    title = parsed.title || title;
    description = parsed.description;
    body = mdxToPlain(parsed.body);
  }

  const path = id === 'index' ? '/' : `/${id}/`;
  const url = new URL(path, site ?? 'https://docs.carina.dev').href;

  const md = [
    '---',
    `title: ${JSON.stringify(title)}`,
    description ? `description: ${JSON.stringify(description)}` : null,
    `source: ${url}`,
    '---',
    '',
    `# ${title}`,
    '',
    description ? `> ${description}\n` : null,
    body,
    '',
    '---',
    `Source: ${url}`,
    `Markdown: ${url}index.md`,
    '',
  ]
    .filter((x) => x !== null)
    .join('\n');

  return new Response(md, {
    headers: {
      'Content-Type': 'text/markdown; charset=utf-8',
      'Cache-Control': 'public, max-age=300',
      'X-Robots-Tag': 'noindex',
    },
  });
};
