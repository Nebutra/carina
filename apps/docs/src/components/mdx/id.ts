let n = 0;

/** Stable-enough unique id for client widgets on a page. */
export function uniqueId(prefix: string): string {
  n += 1;
  return `${prefix}-${n}`;
}
