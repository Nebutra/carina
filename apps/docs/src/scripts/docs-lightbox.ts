/**
 * Dependency-free image lightbox for docs content (Mintlify/Fumadocs zoom).
 *
 * Eligible: `.sl-markdown-content img` and `.docs-frame__media img`, except
 * images wrapped in links. Uses a native `<dialog>` (focus trap, Esc, and
 * `aria-modal` for free). Styles live in `src/styles/polish/content.css`
 * (`.docs-lightbox*`).
 */

let lastTrigger: HTMLElement | null = null;

function ensureDialog(): HTMLDialogElement {
  const existing = document.querySelector<HTMLDialogElement>('dialog.docs-lightbox');
  if (existing) return existing;

  const dialog = document.createElement('dialog');
  dialog.className = 'docs-lightbox';
  dialog.setAttribute('aria-label', 'Image viewer');

  const img = document.createElement('img');
  img.className = 'docs-lightbox__img';
  img.alt = '';

  const close = document.createElement('button');
  close.type = 'button';
  close.className = 'docs-lightbox__close';
  close.setAttribute('aria-label', 'Close image viewer');
  close.innerHTML =
    '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';

  dialog.append(close, img);
  // Click anywhere (backdrop, image, or button) dismisses; Esc via native dialog.
  dialog.addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => {
    lastTrigger?.focus({ preventScroll: true });
    lastTrigger = null;
  });

  document.body.appendChild(dialog);
  return dialog;
}

function openLightbox(source: HTMLImageElement): void {
  const dialog = ensureDialog();
  const img = dialog.querySelector<HTMLImageElement>('.docs-lightbox__img');
  if (!img) return;
  img.src = source.currentSrc || source.src;
  img.alt = source.alt || '';
  lastTrigger = source;
  dialog.showModal();
  dialog.querySelector<HTMLButtonElement>('.docs-lightbox__close')?.focus();
}

/** Idempotent: safe to call on every astro:page-load. */
export function initLightbox(): void {
  const images = document.querySelectorAll<HTMLImageElement>(
    '.sl-markdown-content img, .docs-frame__media img',
  );
  images.forEach((img) => {
    if (img.dataset.lightbox === '1') return;
    if (img.closest('a[href]')) return; // linked images keep their link behavior
    img.dataset.lightbox = '1';
    img.classList.add('docs-lightbox-target');
    // Keyboard operability + a real focus target for close-restore.
    img.tabIndex = 0;
    img.setAttribute('role', 'button');
    if (!img.getAttribute('aria-label')) {
      img.setAttribute('aria-label', img.alt ? `View image: ${img.alt}` : 'View image');
    }
    img.addEventListener('click', () => openLightbox(img));
    img.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        openLightbox(img);
      }
    });
  });
}
