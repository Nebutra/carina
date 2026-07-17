/**
 * Wrap every markdown <table> in <div class="docs-table-scroll"> so the
 * wrapper owns horizontal overflow and the table keeps NATIVE table layout.
 *
 * Why: the previous CSS-only approach (`table { display: block }` +
 * `thead/tbody/tr { display: table }`) made every row an independent table,
 * so column widths were computed per-row and never aligned across rows.
 * Mintlify (scroll-area wrapper, min-width: fit-content) and fumadocs-ui
 * (overflow-auto div) both use the wrapper pattern — tables stay tables.
 *
 * Hand-rolled walker: no unist-util-visit dependency.
 */
export default function rehypeTableScroll() {
  return (tree) => {
    walk(tree);
  };
}

function walk(node) {
  if (!node || !Array.isArray(node.children)) return;
  for (let i = 0; i < node.children.length; i++) {
    const child = node.children[i];
    if (
      child.type === 'element' &&
      child.tagName === 'table' &&
      !(node.type === 'element' && hasClass(node, 'docs-table-scroll'))
    ) {
      node.children[i] = {
        type: 'element',
        tagName: 'div',
        properties: { className: ['docs-table-scroll'] },
        children: [child],
      };
      continue; // wrapped table needs no descent (tables don't nest in md)
    }
    walk(child);
  }
}

function hasClass(node, name) {
  const cls = node.properties && node.properties.className;
  return Array.isArray(cls) ? cls.includes(name) : cls === name;
}
