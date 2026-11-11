/** Unsaved-changes detection by comparing serialised form snapshots. */

export function snapshotOf(values: Record<string, unknown>): string {
  return JSON.stringify(values);
}

/** True when the form differs from its baseline and has not just been saved. */
export function isDirty(baseline: string, current: string, saved: boolean): boolean {
  return !saved && baseline !== current;
}
