/** Pure helpers to keep page filter state in the query string. */

/** Value of `key` if it is one of `allowed`, else `fallback`. */
export function enumParam<T extends string>(
  sp: URLSearchParams,
  key: string,
  allowed: readonly T[],
  fallback: T,
): T {
  const v = sp.get(key);
  return v !== null && (allowed as readonly string[]).includes(v) ? (v as T) : fallback;
}

export function flagParam(sp: URLSearchParams, key: string): boolean {
  return sp.get(key) === '1';
}

/**
 * New query string (with leading "?", or "" when empty) after applying `updates` to `current`.
 * null, undefined, '' and false remove the key (so defaults never clutter the URL); true is
 * written as "1". Unrelated keys (for example `host_id`) are kept.
 */
export function buildSearch(
  current: URLSearchParams,
  updates: Record<string, string | boolean | null | undefined>,
): string {
  const sp = new URLSearchParams(current);
  for (const [k, v] of Object.entries(updates)) {
    if (v === null || v === undefined || v === '' || v === false) sp.delete(k);
    else sp.set(k, v === true ? '1' : v);
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

export interface FleetUrlFilters {
  network: string;
  q: string;
  conflicts: boolean;
  alerts: boolean;
  hidden: boolean;
}

export function fleetFiltersFromParams(sp: URLSearchParams): FleetUrlFilters {
  return {
    network: sp.get('network') ?? '',
    q: sp.get('q') ?? '',
    conflicts: flagParam(sp, 'conflicts'),
    alerts: flagParam(sp, 'alerts'),
    hidden: flagParam(sp, 'hidden'),
  };
}

export function fleetFiltersToUpdates(f: FleetUrlFilters): Record<string, string | boolean> {
  return {
    network: f.network,
    q: f.q.trim(),
    conflicts: f.conflicts,
    alerts: f.alerts,
    hidden: f.hidden,
  };
}
