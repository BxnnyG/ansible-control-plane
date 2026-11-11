/**
 * In-memory stale-while-revalidate cache for workspace list GETs (never persisted).
 *
 * Only plain collection URLs of an allowlist are cacheable; detail endpoints (anything with an id
 * or sub-path, in particular secret, vault and webhook details) are never stored. The secrets list
 * is metadata only. Any mutation drops every entry of its workspace; logout and workspace switch
 * clear everything.
 */

const LISTS = [
  'secrets',
  'vault-identities',
  'projects',
  'templates',
  'inventories',
  'connectors',
  'schedules',
] as const;

const WS_RE = /^\/api\/v1\/workspaces\/([^/?#]+)(\/[^?#]*)?(\?.*)?$/;

/** Workspace slug of an API path, or null for non-workspace paths. */
export function workspaceOf(path: string): string | null {
  const m = WS_RE.exec(path);
  return m ? decodeURIComponent(m[1]) : null;
}

/** True when `path` is a collection URL (no id, no sub-path, no query) that may be cached. */
export function isCacheableList(path: string): boolean {
  const m = WS_RE.exec(path);
  if (!m || m[3]) return false;
  const rest = (m[2] ?? '').replace(/^\//, '');
  return (LISTS as readonly string[]).includes(rest);
}

/** Cache key: workspace plus URL. */
export function cacheKey(path: string): string {
  return `${workspaceOf(path) ?? ''}|${path}`;
}

export class ApiCache {
  private map = new Map<string, unknown>();
  private gen = 0;

  /** Generation token; pass it back to `set` so responses that raced a mutation are dropped. */
  token(): number {
    return this.gen;
  }

  get(key: string): unknown {
    return this.map.get(key);
  }

  set(key: string, value: unknown, token: number): void {
    if (token === this.gen) this.map.set(key, value);
  }

  /** Drops every entry of one workspace. */
  invalidateWorkspace(ws: string): void {
    this.gen++;
    const prefix = `${ws}|`;
    for (const k of [...this.map.keys()]) if (k.startsWith(prefix)) this.map.delete(k);
  }

  clear(): void {
    this.gen++;
    this.map.clear();
  }

  get size(): number {
    return this.map.size;
  }
}

export const apiCache = new ApiCache();
