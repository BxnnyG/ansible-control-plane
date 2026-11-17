import type {
  ReachabilityMatrix,
  ReachabilityResult,
  ReachabilityTarget,
  ReachabilityTargetInput,
} from './api';

export type CellState = 'ok' | 'failed' | 'probe_unreachable' | 'none';

export interface CellView {
  state: CellState;
  /** Short label (also the accessible name). */
  label: string;
  /** Single-character glyph so the state is not conveyed by color alone. */
  icon: string;
  /** Tone class from app.css. */
  tone: string;
  stale: boolean;
  /** Multi-line text for the tooltip. */
  title: string;
}

/** Key of a result in the lookup map. */
export function cellKey(sourceId: string, targetId: string): string {
  return `${sourceId}:${targetId}`;
}

/** Index results by source and target. */
export function indexResults(
  results: readonly ReachabilityResult[],
): Map<string, ReachabilityResult> {
  const m = new Map<string, ReachabilityResult>();
  for (const r of results) m.set(cellKey(r.source_id, r.target_id), r);
  return m;
}

/** A result is stale when older than twice the interval; never stale when the scheduler is off. */
export function isStale(
  checkedAt: string,
  intervalSeconds: number,
  now: number = Date.now(),
): boolean {
  if (intervalSeconds <= 0) return false;
  const t = Date.parse(checkedAt);
  if (Number.isNaN(t)) return false;
  return now - t > 2 * intervalSeconds * 1000;
}

const LABELS: Record<CellState, { label: string; icon: string; tone: string }> = {
  ok: { label: 'Reachable', icon: '✓', tone: 'tone-ok' },
  failed: { label: 'Failed', icon: '✕', tone: 'tone-bad' },
  probe_unreachable: { label: 'Probe host unreachable', icon: '?', tone: 'tone-neutral' },
  none: { label: 'Not checked', icon: '', tone: '' },
};

/** Display model of one matrix cell. */
export function cellView(
  r: ReachabilityResult | undefined,
  intervalSeconds: number,
  now: number = Date.now(),
): CellView {
  if (!r) return { state: 'none', ...LABELS.none, stale: false, title: 'Not checked yet' };
  const base = LABELS[r.status];
  const stale = isStale(r.checked_at, intervalSeconds, now);
  const lines = [base.label];
  if (r.latency_ms != null) lines.push(`Latency: ${r.latency_ms} ms`);
  if (r.detail) lines.push(r.detail);
  lines.push(`Checked: ${r.checked_at}`);
  if (stale) lines.push('Stale: older than twice the interval');
  return { state: r.status, ...base, stale, title: lines.join('\n') };
}

/** Link to the run that produced a result, if any. */
export function runHref(r: ReachabilityResult | undefined): string | null {
  return r?.run_id ? `/runs/${r.run_id}` : null;
}

/** Human readable interval, "Off" for 0. */
export function intervalLabel(seconds: number): string {
  if (seconds <= 0) return 'Off';
  if (seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
}

/** Interval is valid when 0 (off) or within 60..86400 seconds. */
export function validInterval(seconds: number): boolean {
  return Number.isInteger(seconds) && (seconds === 0 || (seconds >= 60 && seconds <= 86400));
}

/** Short description of what a target checks. */
export function targetSummary(t: ReachabilityTarget): string {
  switch (t.check) {
    case 'tcp':
      return `tcp ${t.address}:${t.port ?? ''}`;
    case 'dns':
      return `dns ${t.dns_name}`;
    default:
      return `icmp ${t.address}`;
  }
}

export interface TargetDraft {
  name: string;
  address: string;
  check: 'icmp' | 'tcp' | 'dns';
  port: string;
  dns_name: string;
  enabled: boolean;
}

export function emptyDraft(): TargetDraft {
  return { name: '', address: '', check: 'icmp', port: '', dns_name: '', enabled: true };
}

export function draftFrom(t: ReachabilityTarget): TargetDraft {
  return {
    name: t.name,
    address: t.address,
    check: t.check,
    port: t.port == null ? '' : String(t.port),
    dns_name: t.dns_name,
    enabled: t.enabled,
  };
}

/** Validation message, or null when the draft can be submitted. */
export function draftError(d: TargetDraft): string | null {
  if (!d.name.trim()) return 'Name is required.';
  if (!d.address.trim()) return 'Address is required.';
  if (d.check === 'tcp') {
    const p = Number(d.port);
    if (!Number.isInteger(p) || p < 1 || p > 65535) return 'TCP checks need a port (1-65535).';
  }
  if (d.check === 'dns' && !d.dns_name.trim()) return 'DNS checks need a name to resolve.';
  return null;
}

export function draftToInput(d: TargetDraft): ReachabilityTargetInput {
  return {
    name: d.name.trim(),
    address: d.address.trim(),
    check: d.check,
    port: d.check === 'tcp' ? Number(d.port) : null,
    dns_name: d.check === 'dns' ? d.dns_name.trim() : '',
    enabled: d.enabled,
  };
}

/** Targets shown as matrix columns (enabled ones only). */
export function visibleTargets(m: ReachabilityMatrix): ReachabilityTarget[] {
  return m.targets.filter((t) => t.enabled);
}
