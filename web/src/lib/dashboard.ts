import type { Connector, FleetHost, Run } from './api';

const DAY_MS = 24 * 60 * 60 * 1000;

export interface RunStats {
  /** Runs created in the last 24 hours (within the loaded page). */
  total: number;
  ok: number;
  failed: number;
  running: number;
  queued: number;
}

/** Counts runs of the last 24 hours; failed includes lost runs. */
export function runStats(runs: readonly Run[], now: number): RunStats {
  const s: RunStats = { total: 0, ok: 0, failed: 0, running: 0, queued: 0 };
  for (const r of runs) {
    if (r.status === 'running') s.running++;
    else if (r.status === 'queued') s.queued++;
    if (now - Date.parse(r.created_at) > DAY_MS) continue;
    s.total++;
    if (r.status === 'succeeded') s.ok++;
    else if (r.status === 'failed' || r.status === 'lost') s.failed++;
  }
  return s;
}

/** Oldest to newest finished-run outcomes for the sparkline (true = succeeded). */
export function recentOutcomes(runs: readonly Run[], max = 24): boolean[] {
  return runs
    .filter((r) => r.status === 'succeeded' || r.status === 'failed' || r.status === 'lost')
    .slice(0, max)
    .map((r) => r.status === 'succeeded')
    .reverse();
}

export interface FailingTemplate {
  id: string | null;
  name: string;
  failures: number;
  lastRunId: string;
}

/** Templates with failed runs among the given runs, worst first. */
export function failingTemplates(runs: readonly Run[], limit = 5): FailingTemplate[] {
  const by = new Map<string, FailingTemplate>();
  for (const r of runs) {
    if (r.status !== 'failed' && r.status !== 'lost') continue;
    const key = r.template_id ?? r.template_name;
    const cur = by.get(key);
    if (cur) cur.failures++;
    else by.set(key, { id: r.template_id, name: r.template_name, failures: 1, lastRunId: r.id });
  }
  return [...by.values()].sort((a, b) => b.failures - a.failures).slice(0, limit);
}

export interface FleetStats {
  total: number;
  online: number;
  drift: number;
  unmanaged: number;
}

export function fleetStats(hosts: readonly FleetHost[]): FleetStats {
  return {
    total: hosts.length,
    online: hosts.filter((h) => h.online === 'online').length,
    drift: hosts.filter((h) => h.drift !== null).length,
    unmanaged: hosts.filter((h) => !h.managed).length,
  };
}

export interface ConnectorHealth {
  ok: number;
  error: number;
  pending: number;
  disabled: number;
  warnings: number;
}

export function connectorHealth(items: readonly Connector[]): ConnectorHealth {
  const h: ConnectorHealth = { ok: 0, error: 0, pending: 0, disabled: 0, warnings: 0 };
  for (const c of items) {
    if (!c.enabled || c.status === 'disabled') h.disabled++;
    else if (c.status === 'ok') h.ok++;
    else if (c.status === 'error') h.error++;
    else h.pending++;
    h.warnings += warningsOf(c).length;
  }
  return h;
}

/** All warnings of a connector (its own and those reported by the last sync). */
export function warningsOf(c: Pick<Connector, 'warnings' | 'counts'>): string[] {
  return [...(c.warnings ?? []), ...(c.counts?.warnings ?? [])];
}

/** Short relative time such as "5 min ago"; falls back to the date for old values. */
export function relativeTime(iso: string | null | undefined, now: number): string {
  if (!iso) return '-';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 45) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} h ago`;
  const d = Math.round(h / 24);
  if (d < 14) return `${d} d ago`;
  return new Date(t).toLocaleDateString();
}
