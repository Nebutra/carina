/**
 * Shared GSAP motion helpers for docs page enter + tab panel crossfades.
 * Respects prefers-reduced-motion (duration 0 / skip transforms).
 */
import gsap from 'gsap';

gsap.defaults({ ease: 'power2.out', duration: 0.28 });

export function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined') return true;
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

function pageRoot(): HTMLElement | null {
  return (
    document.querySelector<HTMLElement>('[data-page-enter]') ??
    document.querySelector<HTMLElement>('.main-pane main') ??
    document.querySelector<HTMLElement>('main .content-panel') ??
    document.querySelector<HTMLElement>('main')
  );
}

/** Instant hide before paint (astro:after-swap) so enter tween has a clean start. */
export function preparePageEnter(): void {
  const el = pageRoot();
  if (!el || prefersReducedMotion()) return;
  gsap.killTweensOf(el);
  gsap.set(el, { autoAlpha: 0, y: 10 });
}

/** Fade + slight rise for primary page content. */
export function runPageEnter(): void {
  const el = pageRoot();
  if (!el) {
    document.documentElement.setAttribute('data-vt-ready', '');
    return;
  }
  gsap.killTweensOf(el);

  const done = () => {
    document.documentElement.setAttribute('data-vt-ready', '');
  };

  if (prefersReducedMotion()) {
    gsap.set(el, { autoAlpha: 1, y: 0, clearProps: 'transform,opacity,visibility' });
    done();
    return;
  }

  gsap.fromTo(
    el,
    { autoAlpha: 0, y: 10 },
    {
      autoAlpha: 1,
      y: 0,
      duration: 0.34,
      ease: 'power2.out',
      overwrite: 'auto',
      clearProps: 'transform',
      onComplete: done,
    },
  );
}

export type CrossfadeOptions = {
  from: HTMLElement | null | undefined;
  to: HTMLElement;
  /** Called once before the incoming panel is revealed (update aria, tab classes). */
  onSwitch?: () => void;
};

/**
 * Sequential panel crossfade: out → swap → in.
 * Uses autoAlpha (opacity + visibility). Caller should only set `hidden`
 * via onSwitch/after; do not leave `hidden` on `to` before the tween.
 */
export function crossfadePanel({ from, to, onSwitch }: CrossfadeOptions): void {
  const reduce = prefersReducedMotion();
  const outDur = reduce ? 0 : 0.12;
  const inDur = reduce ? 0 : 0.2;

  const targets = [from, to].filter((n): n is HTMLElement => Boolean(n));
  gsap.killTweensOf(targets);

  const finishFrom = () => {
    if (!from || from === to) return;
    from.hidden = true;
    from.classList.remove('is-active');
    gsap.set(from, { clearProps: 'opacity,visibility,transform' });
  };

  const showTo = () => {
    onSwitch?.();
    to.hidden = false;
    to.classList.add('is-active');
  };

  if (!from || from === to) {
    showTo();
    if (reduce) {
      gsap.set(to, { autoAlpha: 1, y: 0, clearProps: 'transform' });
      return;
    }
    gsap.fromTo(
      to,
      { autoAlpha: 0, y: 6 },
      {
        autoAlpha: 1,
        y: 0,
        duration: inDur,
        ease: 'power2.out',
        clearProps: 'transform',
      },
    );
    return;
  }

  const tl = gsap.timeline({ defaults: { overwrite: 'auto' } });

  tl.to(from, {
    autoAlpha: 0,
    y: -4,
    duration: outDur,
    ease: 'power2.in',
    onComplete: finishFrom,
  });

  tl.add(() => {
    showTo();
  });

  tl.fromTo(
    to,
    { autoAlpha: 0, y: 8 },
    {
      autoAlpha: 1,
      y: 0,
      duration: inDur,
      ease: 'power2.out',
      clearProps: 'transform',
    },
  );
}

/** Kill page tweens before View Transition swap to avoid orphaned styles. */
export function killPageMotion(): void {
  const el = pageRoot();
  if (el) gsap.killTweensOf(el);
}
