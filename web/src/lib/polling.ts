/** Background refresh interval of the dashboard (ms). */
export const DASHBOARD_POLL_MS = 60_000;

/**
 * Whether a periodic refresh should run now: never while the tab is hidden, otherwise once the
 * last load is at least `minAge` ms old (used when the tab becomes visible again).
 */
export function shouldRefresh(
  hidden: boolean,
  lastLoad: number,
  now: number,
  minAge: number,
): boolean {
  return !hidden && now - lastLoad >= minAge;
}
