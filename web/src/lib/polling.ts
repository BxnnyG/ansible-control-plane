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

export interface PollOptions {
  intervalMs: number;
  maxMs: number;
  /** Injectable for tests. */
  sleep?: (ms: number) => Promise<void>;
}

/**
 * Calls `fetch` until `done(result)` is true or `maxMs` has elapsed. Returns the last result and
 * whether the condition was met. Fetch errors end the polling (met = false, result = undefined).
 */
export async function pollUntil<T>(
  fetch: () => Promise<T>,
  done: (v: T) => boolean,
  { intervalMs, maxMs, sleep = (ms) => new Promise((r) => setTimeout(r, ms)) }: PollOptions,
): Promise<{ met: boolean; value: T | undefined }> {
  let waited = 0;
  let value: T | undefined;
  for (;;) {
    await sleep(intervalMs);
    waited += intervalMs;
    try {
      value = await fetch();
    } catch {
      return { met: false, value: undefined };
    }
    if (done(value)) return { met: true, value };
    if (waited >= maxMs) return { met: false, value };
  }
}
