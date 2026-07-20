/**
 * Search modal enhancement — Mintlify command-palette parity.
 *
 * Progressive enhancement over Starlight's built-in Pagefind dialog
 * (`site-search`). Pagefind UI only exists in production builds; everything
 * here degrades safely in dev (the Ask-AI row stays hidden without input).
 *
 * Injects, inside the dialog frame:
 *  - a decorative keyboard hint bar (↑↓ Select · ↵ Open · esc Close).
 *
 * Also wires arrow-key result navigation (so the hints are true) and decorates
 * nested Pagefind sub-results with a muted "Parent ›" breadcrumb prefix taken
 * from the sibling top-level result title already present in the DOM.
 */

type Labels = {
  select: string;
  open: string;
  close: string;
};

function labels(): Labels {
  const zh = (document.documentElement.lang || '').toLowerCase().startsWith('zh');
  return zh
    ? { select: '选择', open: '打开', close: '关闭' }
    : { select: 'Select', open: 'Open', close: 'Close' };
}

function enhanceSearch(): void {
  const siteSearch = document.querySelector('site-search');
  const dialog = siteSearch?.querySelector('dialog');
  const frame = siteSearch?.querySelector<HTMLElement>('.dialog-frame');
  if (!siteSearch || !dialog || !frame) return;
  // Idempotent per DOM (ClientRouter swaps the body on soft navigation).
  if (frame.querySelector('.docs-search-foot')) return;

  const t = labels();

  /* ── Footer: keyboard hint bar ──────────────────────────────────── */

  const foot = document.createElement('div');
  foot.className = 'docs-search-foot';

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

  foot.append(hints);
  frame.append(foot);

  /* ── Query tracking (input is created later by PagefindUI) ──────── */

  const getInput = () =>
    frame.querySelector<HTMLInputElement>('.pagefind-ui__search-input');

  const syncQuery = () => {
    getInput()?.setAttribute('aria-keyshortcuts', 'ArrowDown ArrowUp Enter');
  };
  frame.addEventListener('input', syncQuery);
  // Pagefind's clear button updates the input without an `input` event.
  frame.addEventListener('click', () => requestAnimationFrame(syncQuery));
  dialog.addEventListener('close', () => requestAnimationFrame(syncQuery));

  /* ── Keyboard navigation (makes the hint bar honest) ────────────── */

  const focusables = (): HTMLElement[] => {
    return Array.from(frame.querySelectorAll<HTMLElement>('.pagefind-ui__result-link'));
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
    // ClientRouter swaps the body; drop the observer once this frame is gone.
    if (!frame.isConnected) {
      observer.disconnect();
      return;
    }
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
