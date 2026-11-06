import type {
  AlertSeverity,
  FleetAlert,
  FleetConflict,
  FleetHost,
  FleetHostDetail,
  FleetNetworkCounts,
  FleetSource,
} from './api';

export interface FieldRow {
  key: string;
  label: string;
  values: string[];
  /** Connector names that provided the value (winner for scalars, all for lists). */
  sources: string[];
}

const FIELDS: { key: keyof Omit<FleetHostDetail['fields'], 'sources'>; label: string }[] = [
  { key: 'kind', label: 'Kind' },
  { key: 'status', label: 'Status' },
  { key: 'hostname', label: 'Hostname' },
  { key: 'fqdn', label: 'FQDN' },
  { key: 'ips', label: 'IP addresses' },
  { key: 'macs', label: 'MAC addresses' },
  { key: 'tags', label: 'Tags' },
  { key: 'interface', label: 'Interface' },
  { key: 'notes', label: 'Notes' },
];

function names(src: FleetSource[] | undefined): string[] {
  return [...new Set((src ?? []).map((s) => s.connector))];
}

/** Merged host fields with their source connectors; empty fields are skipped. */
export function fieldRows(detail: Pick<FleetHostDetail, 'fields'>): FieldRow[] {
  const rows: FieldRow[] = [];
  for (const { key, label } of FIELDS) {
    const raw = detail.fields?.[key];
    const values = Array.isArray(raw) ? raw : raw ? [raw] : [];
    if (values.length === 0) continue;
    rows.push({ key, label, values, sources: names(detail.fields.sources?.[key]) });
  }
  return rows;
}

const CONFLICT_LABEL: Record<string, string> = {
  duplicate_ip: 'Duplicate IP address',
  duplicate_mac: 'Duplicate MAC address',
  name_mismatch: 'Name mismatch',
  kind_mismatch: 'Kind mismatch',
  mac_conflict: 'MAC conflict',
  ip_conflict: 'IP conflict',
};

export function conflictLabel(type: string): string {
  return CONFLICT_LABEL[type] ?? type.replace(/_/g, ' ');
}

/** Compact "key: value" rendering of conflict details; nested values are JSON. */
export function conflictDetails(c: Pick<FleetConflict, 'details'>): string[] {
  return Object.entries(c.details ?? {}).map(([k, v]) => {
    const text = typeof v === 'string' ? v : JSON.stringify(v);
    return `${k}: ${text}`;
  });
}

export type ConflictFilter = 'all' | 'open' | 'acknowledged' | 'resolved';

/** Query value for the API; 'all' sends no state filter. */
export function stateParam(f: ConflictFilter): 'open' | 'acknowledged' | 'resolved' | undefined {
  return f === 'all' ? undefined : f;
}

/** Whether the Acknowledge button applies. */
export function canAck(c: Pick<FleetConflict, 'state'>): boolean {
  return c.state === 'open';
}

/** Link labels for the hosts of a conflict; falls back to "host N" without a name. */
export function conflictHosts(
  c: Pick<FleetConflict, 'host_ids' | 'hosts'>,
): { id: string; label: string }[] {
  const names = new Map((c.hosts ?? []).map((h) => [h.id, h.name]));
  return c.host_ids.map((id, i) => ({ id, label: names.get(id) || `host ${i + 1}` }));
}

const SEVERITY_LABEL: Record<AlertSeverity, string> = {
  critical: 'Critical',
  warning: 'Warning',
  unknown: 'Unknown',
  info: 'Info',
};

export function severityLabel(s: string): string {
  return SEVERITY_LABEL[s as AlertSeverity] ?? s;
}

/** Tailwind classes of a severity badge. */
export function severityClass(s: string): string {
  switch (s) {
    case 'critical':
      return 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300';
    case 'warning':
      return 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300';
    case 'unknown':
      return 'bg-orange-100 text-orange-800 dark:bg-orange-950 dark:text-orange-300';
    default:
      return 'bg-surface-2 text-muted';
  }
}

/** Text of the alert badge in the host list; empty without open alerts. */
export function alertBadge(h: { open_alerts?: number; worst_severity?: string }): string {
  const n = h.open_alerts ?? 0;
  if (n <= 0) return '';
  return `${n} ${severityLabel(h.worst_severity ?? '').toLowerCase()}`.trim();
}

/** Host cell of an alert: merged host name, else what the source reported. */
export function alertHost(a: Pick<FleetAlert, 'host' | 'host_name' | 'host_ip'>): string {
  return a.host || a.host_name || a.host_ip || '-';
}

/** Compact "key=value" labels, sorted by key. */
export function alertLabels(a: Pick<FleetAlert, 'labels'>): string[] {
  return Object.entries(a.labels ?? {})
    .sort(([x], [y]) => x.localeCompare(y))
    .map(([k, v]) => `${k}=${v}`);
}

const ONLINE_LABEL: Record<string, string> = {
  online: 'Online',
  offline: 'Offline',
  unknown: 'Unknown',
};

export function onlineLabel(o: string): string {
  return ONLINE_LABEL[o] ?? o;
}

/** Tailwind classes of the online badge. */
export function onlineClass(o: string): string {
  switch (o) {
    case 'online':
      return 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300';
    case 'offline':
      return 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300';
    default:
      return 'bg-surface-2 text-muted';
  }
}

/** Short Ansible column text: last run result, or why there is none. */
export function ansibleLabel(h: Pick<FleetHost, 'managed' | 'last_run'>): string {
  if (!h.managed) return 'not managed';
  if (!h.last_run) return 'never run';
  return h.last_run.check ? `${h.last_run.result} (check)` : h.last_run.result;
}

/** Tailwind classes of the Ansible badge. */
export function ansibleClass(h: Pick<FleetHost, 'managed' | 'last_run'>): string {
  const r = h.last_run?.result;
  if (r === 'failed' || r === 'unreachable') {
    return 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300';
  }
  if (r === 'ok' || r === 'changed') {
    return 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300';
  }
  return 'bg-surface-2 text-muted';
}

/** Text of the drift badge; empty without drift. */
export function driftLabel(h: Pick<FleetHost, 'drift'>): string {
  const n = h.drift?.changed ?? 0;
  return n > 0 ? `drift (${n} change${n === 1 ? '' : 's'})` : '';
}

/** One-line status counters of a network card. */
export function networkCountsLabel(c: FleetNetworkCounts | undefined): string {
  if (!c) return '';
  return `${c.online} online, ${c.ansible_ok} ok, ${c.failed} failed, ${c.never_managed} never managed`;
}
