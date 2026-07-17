/**
 * Search modal enhancement — Mintlify command-palette parity.
 *
 * Progressive enhancement over Starlight's built-in Pagefind dialog
 * (`site-search`). Pagefind UI only exists in production builds; everything
 * here degrades safely in dev (the Ask-AI row stays hidden without input).
 *
 * Injects, inside the dialog frame:
 *  - an "Ask AI" row pinned under the results (visible when query non-empty);
 *    clicking closes the dialog and dispatches the PUBLIC CONTRACT event
 *    `carina:ask-ai` with `{ question }` (consumed by DocsAssistant).
 *  - a decorative keyboard hint bar (↑↓ Select · ↵ Open · esc Close).
 *
 * Also wires arrow-key result navigation (so the hints are true) and decorates
 * nested Pagefind sub-results with a muted "Parent ›" breadcrumb prefix taken
 * from the sibling top-level result title already present in the DOM.
 */

type Labels = {
  ask: string;
  select: string;
  open: string;
  close: string;
};

function labels(): Labels {
  const zh = (document.documentElement.lang || '').toLowerCase().startsWith('zh');
  return zh
    ? { ask: '问 AI', select: '选择', open: '打开', close: '关闭' }
    : { ask: 'Ask AI', select: 'Select', open: 'Open', close: 'Close' };
}

function enhanceSearch(): void {
  const siteSearch = document.querySelector('site-search');
  const dialog = siteSearch?.querySelector('dialog');
  const frame = siteSearch?.querySelector<HTMLElement>('.dialog-frame');
  if (!siteSearch || !dialog || !frame) return;
  // Idempotent per DOM (ClientRouter swaps the body on soft navigation).
  if (frame.querySelector('.docs-search-foot')) return;

  const t = labels();

  /* ── Footer: Ask-AI row + keyboard hint bar ─────────────────────── */

  const foot = document.createElement('div');
  foot.className = 'docs-search-foot';

  const ask = document.createElement('button');
  ask.type = 'button';
  ask.className = 'docs-search-ask';
  ask.hidden = true;
  const askIcon = document.createElement('span');
  askIcon.className = 'docs-search-ask__icon';
  askIcon.setAttribute('aria-hidden', 'true');
  askIcon.textContent = '✦';
  const askText = document.createElement('span');
  askText.className = 'docs-search-ask__text';
  askText.append(`${t.ask}: `);
  const askQuery = document.createElement('q');
  askQuery.className = 'docs-search-ask__q';
  askText.append(askQuery);
  ask.append(askIcon, askText);

  const hints = document.createElement('div');
  hints.className = 'docs-search-hints';
  hints.setAttribute('aria-hidden', 'true');
  const hint = (keys: string[], label: string) => {
    const item = document.createElement('span');
    item.className = 'docs-search-hints__item';
    for (const k of keys) {
      const kbd = document.createElement('kbd');
      kbd.textContent = k;
      item.append(kbd);
    }
    item.append(` ${label}`);
    return item;
  };
  hints.append(hint(['↑', '↓'], t.select), hint(['↵'], t.open), hint(['esc'], t.close));

  foot.append(ask, hints);
  frame.append(foot);

  /* ── Query tracking (input is created later by PagefindUI) ──────── */

  const getInput = () =>
    frame.querySelector<HTMLInputElement>('.pagefind-ui__search-input');

  let query = '';
  const syncQuery = () => {
    const q = getInput()?.value.trim() ?? '';
    if (q === query) return;
    query = q;
    askQuery.textContent = q;
    ask.hidden = q.length === 0;
    ask.setAttribute('aria-label', `${t.ask}: ${q}`);
  };
  frame.addEventListener('input', syncQuery);
  // Pagefind's clear button updates the input without an `input` event.
  frame.addEventListener('click', () => requestAnimationFrame(syncQuery));
  dialog.addEventListener('close', () => requestAnimationFrame(syncQuery));

  /* ── Ask-AI dispatch (public contract: `carina:ask-ai`) ─────────── */

  ask.addEventListener('click', () => {
    const question = query;
    try {
      dialog.close();
    } catch {
      /* ignore */
    }
    window.dispatchEvent(new CustomEvent('carina:ask-ai', { detail: { question } }));
  });

  /* ── Keyboard navigation (makes the hint bar honest) ────────────── */

  const focusables = (): HTMLElement[] => {
    const items = Array.from(
      frame.querySelectorAll<HTMLElement>('.pagefind-ui__result-link'),
    );
    if (!ask.hidden) items.push(ask);
    return items;
  };

  dialog.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && document.activeElement === getInput()) {
      // Mintlify behavior: Enter in the input opens the first result.
      const first = frame.querySelector<HTMLAnchorElement>('.pagefind-ui__result-link');
      if (first) {
        e.preventDefault();
        first.click();
      }
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const items = focusables();
    if (!items.length) return;
    e.preventDefault();
    const current = items.indexOf(document.activeElement as HTMLElement);
    let next: number;
    if (current === -1) {
      next = e.key === 'ArrowDown' ? 0 : items.length - 1;
    } else {
      next = e.key === 'ArrowDown' ? current + 1 : current - 1;
    }
    if (next < 0) {
      getInput()?.focus();
      return;
    }
    if (next >= items.length) next = items.length - 1;
    items[next]?.focus();
    items[next]?.scrollIntoView({ block: 'nearest' });
  });

  /* ── Nested-result breadcrumbs (parent page title from DOM) ─────── */

  const decorateCrumbs = () => {
    for (const nested of frame.querySelectorAll<HTMLElement>('.pagefind-ui__result-nested')) {
      const inner = nested.closest('.pagefind-ui__result-inner');
      const parent = inner
        ?.querySelector(':scope > .pagefind-ui__result-title > .pagefind-ui__result-link')
        ?.textContent?.trim();
      const title = nested.querySelector<HTMLElement>('.pagefind-ui__result-title');
      if (!title) continue;
      let crumb = title.querySelector<HTMLElement>(':scope > .docs-search-crumb');
      if (!parent) {
        crumb?.remove();
        continue;
      }
      if (!crumb) {
        crumb = document.createElement('span');
        crumb.className = 'docs-search-crumb';
        crumb.setAttribute('aria-hidden', 'true');
        title.prepend(crumb);
      }
      // Only write when stale so the MutationObserver loop converges.
      if (crumb.textContent !== parent) crumb.textContent = parent;
    }
  };

  const observer = new MutationObserver(() => {
    decorateCrumbs();
    syncQuery();
  });
  observer.observe(frame, { childList: true, subtree: true });
  dialog.addEventListener('close', () => syncQuery());
}

function init(): void {
  enhanceSearch();
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init, { once: true });
} else {
  init();
}
document.addEventListener('astro:page-load', init);
