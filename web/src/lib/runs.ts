import type { Run, RunFilter, RunStatus } from './api';

export const RUN_STATUSES: RunStatus[] = [
  'queued',
  'running',
  'succeeded',
  'failed',
  'cancelled',
  'lost',
];
export const RUN_TRIGGERS = ['manual', 'schedule', 'webhook', 'api'] as const;

export function isFinal(status: RunStatus): boolean {
  return status !== 'queued' && status !== 'running';
}

/** Query string for GET runs; empty filter values are dropped. */
export function buildRunQuery(f: RunFilter): string {
  const p = new URLSearchParams();
  for (const key of ['template', 'status', 'trigger', 'user', 'host', 'cursor'] as const) {
    const v = f[key]?.trim();
    if (v) p.set(key, v);
  }
  if (f.limit && f.limit > 0) p.set('limit', String(f.limit));
  return p.toString();
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isUuid(v: string): boolean {
  return UUID.test(v.trim());
}

/** Human duration such as "1m 05s"; running runs use `now`. '-' when not started. */
export function formatDuration(
  startedAt: string | null,
  finishedAt: string | null,
  now: number = Date.now(),
): string {
  if (!startedAt) return '-';
  const start = Date.parse(startedAt);
  if (Number.isNaN(start)) return '-';
  const end = finishedAt ? Date.parse(finishedAt) : now;
  const total = Math.max(0, Math.round((end - start) / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  if (h > 0) return `${h}h ${pad(m)}m ${pad(s)}s`;
  if (m > 0) return `${m}m ${pad(s)}s`;
  return `${s}s`;
}

/** Whether a live-updated run belongs in a list with the given filters (host is checked on params). */
export function matchesFilter(run: Run, f: RunFilter): boolean {
  if (f.template && run.template_id !== f.template) return false;
  if (f.status && run.status !== f.status) return false;
  if (f.trigger && run.trigger !== f.trigger) return false;
  if (f.user && run.triggered_by !== f.user) return false;
  if (f.host && !(run.params.hosts ?? []).includes(f.host.trim())) return false;
  return true;
}

/**
 * Applies a live run update to a newest-first list. Known runs are replaced in
 * place; new matching runs are inserted by creation time. Runs that no longer
 * match are dropped.
 */
export function applyRunUpdate(list: Run[], run: Run, f: RunFilter): Run[] {
  const idx = list.findIndex((r) => r.id === run.id);
  if (!matchesFilter(run, f)) return idx >= 0 ? list.filter((r) => r.id !== run.id) : list;
  if (idx >= 0) {
    const next = list.slice();
    next[idx] = run;
    return next;
  }
  const at = list.findIndex((r) => r.created_at < run.created_at);
  if (at === -1) return list.length === 0 ? [run] : list; // older than the loaded page
  return [...list.slice(0, at), run, ...list.slice(at)];
}

/** Tailwind classes for a status badge. */
export function statusClass(status: RunStatus): string {
  switch (status) {
    case 'succeeded':
      return 'bg-green-600/15 text-green-700 dark:text-green-400';
    case 'failed':
    case 'lost':
      return 'bg-red-600/15 text-red-700 dark:text-red-400';
    case 'running':
      return 'bg-blue-600/15 text-blue-700 dark:text-blue-400';
    case 'cancelled':
      return 'bg-amber-500/15 text-amber-700 dark:text-amber-400';
    default:
      return 'bg-surface-2 text-muted';
  }
}
