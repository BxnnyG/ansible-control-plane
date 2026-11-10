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
      return 'tone-ok';
    case 'failed':
    case 'lost':
      return 'tone-bad';
    case 'running':
      return 'tone-info tone-pulse';
    case 'cancelled':
      return 'tone-warn';
    default:
      return 'tone-neutral';
  }
}

/** Who started a run: "you", the user's name, or a dash for unattended triggers. */
export function triggeredByLabel(run: Run, meId: string | undefined): string {
  if (!run.triggered_by) return run.trigger === 'manual' || run.trigger === 'api' ? 'unknown' : '-';
  if (run.triggered_by === meId) return 'you';
  return run.triggered_by_name ?? 'unknown user';
}

export interface UserOption {
  id: string;
  label: string;
}

/** Adds the users seen in `runs` to a known id -> display name map (names are never overwritten by null). */
export function collectUsers(
  known: Readonly<Record<string, string>>,
  runs: readonly Run[],
): Record<string, string> {
  const out = { ...known };
  for (const r of runs) {
    if (r.triggered_by && (r.triggered_by_name || !(r.triggered_by in out)))
      out[r.triggered_by] = r.triggered_by_name ?? out[r.triggered_by] ?? '';
  }
  return out;
}

/**
 * Choices for the user filter, built from the users seen in loaded runs (there is
 * no endpoint that lists users). The signed-in user and the currently selected id
 * are always present, so the select never loses its value.
 */
export function userOptions(
  known: Readonly<Record<string, string>>,
  me: { id: string; name?: string } | undefined,
  selected: string,
): UserOption[] {
  const byId = new Map<string, string>();
  for (const [id, name] of Object.entries(known)) byId.set(id, name || 'unknown user');
  if (me) byId.set(me.id, 'you');
  if (selected && !byId.has(selected)) byId.set(selected, 'selected user');
  return [...byId.entries()]
    .map(([id, label]) => ({ id, label }))
    .sort((a, b) => {
      if (a.id === me?.id) return -1;
      if (b.id === me?.id) return 1;
      return a.label.localeCompare(b.label);
    });
}

/** Whether the cancel action applies: run still active, no cancel pending, permission held. */
export function canCancelRun(run: Run, hasPermission: boolean): boolean {
  return (
    hasPermission &&
    (run.status === 'queued' || run.status === 'running') &&
    !run.cancel_requested_at
  );
}
