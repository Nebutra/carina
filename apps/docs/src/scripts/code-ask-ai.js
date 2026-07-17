/**
 * Ask-AI button on Expressive Code frames (Mintlify-style ✦ next to copy).
 *
 * Contract: dispatches `carina:ask-ai` CustomEvent with detail `{ question }`.
 * DocsAssistant owns the listener; if none is mounted the event no-ops silently.
 *
 * Mounted from Head.astro (global); re-runs after ClientRouter navigations.
 * Styling lives in src/styles/polish/code.css (.ec-ask-ai — mirrors .copy button).
 */

const MAX_CODE_CHARS = 4000;

const SPARKLE_SVG =
  '<svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">' +
  '<path d="M8 1.5c.6 3.4 3 5.8 6.5 6.5-3.5.7-5.9 3.1-6.5 6.5-.6-3.4-3-5.8-6.5-6.5C5 7.3 7.4 4.9 8 1.5Z"/>' +
  '</svg>';

/** Reconstruct visible code text (line by line, without gutter numbers). */
function extractCode(frame) {
  const pre = frame.querySelector('pre');
  if (!pre) return '';
  const lines = pre.querySelectorAll('.ec-line .code');
  const text = lines.length
    ? Array.from(lines, (line) => line.textContent ?? '').join('\n')
    : (pre.textContent ?? '');
  return text.length > MAX_CODE_CHARS ? `${text.slice(0, MAX_CODE_CHARS)}\n…` : text;
}

function addAskButton(frame) {
  if (frame.querySelector('.ec-ask-ai')) return;
  const copy = frame.querySelector('.copy');
  if (!copy) return;

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'ec-ask-ai';
  btn.setAttribute('aria-label', 'Ask AI about this code');
  btn.title = 'Ask AI about this code';
  btn.innerHTML = SPARKLE_SVG;

  btn.addEventListener('click', () => {
    const lang = frame.querySelector('pre')?.getAttribute('data-language') ?? '';
    const question = `Explain this code:\n\n\`\`\`${lang}\n${extractCode(frame)}\n\`\`\``;
    window.dispatchEvent(new CustomEvent('carina:ask-ai', { detail: { question } }));
  });

  // Sibling before .copy — both are absolutely positioned inside the frame.
  copy.insertAdjacentElement('beforebegin', btn);
}

function boot() {
  document.querySelectorAll('.expressive-code .frame').forEach(addAskButton);
}

boot();
document.addEventListener('astro:page-load', boot);
