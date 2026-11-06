import type { FleetConflict, FleetHostDetail, FleetSource } from './api';

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
