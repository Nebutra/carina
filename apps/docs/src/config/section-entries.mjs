/** Canonical landing page for each documentation section. */
export const sectionEntries = Object.freeze({
  'getting-started': 'getting-started/introduction',
  concepts: 'concepts/runtime',
  use: 'use/cli-tui',
  agents: 'agents/overview',
  tools: 'tools/overview',
  memory: 'memory/overview',
  workflows: 'workflows/overview',
  api: 'api/overview',
  deployment: 'deployment/local',
  observability: 'observability/traces',
  reference: 'reference/glossary',
});

export const sectionRedirects = Object.fromEntries(
  Object.entries(sectionEntries).flatMap(([section, entry]) => [
    [`/${section}`, `/${entry}/`],
    [`/zh-cn/${section}`, `/zh-cn/${entry}/`],
  ]),
);
