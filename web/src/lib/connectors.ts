import type { Connector, ConnectorStatus, ConnectorType, SecretMeta } from './api';

export type ConfigValue = string | number | boolean;

/** Form state: text inputs per schema field (bool fields use 'true'/'false'). */
export type FieldValues = Record<string, string>;

/** Initial form values for a type, taken from an existing connector when editing. */
export function initialValues(type: ConnectorType, existing?: Connector | null): FieldValues {
  const out: FieldValues = {};
  for (const f of type.fields) {
    const v = existing?.config?.[f.key];
    if (v === undefined || v === null) out[f.key] = f.kind === 'bool' ? 'false' : '';
    else out[f.key] = String(v);
  }
  return out;
}

/** Builds the config object from form values; empty optional fields are omitted. */
export function buildConfig(type: ConnectorType, values: FieldValues): Record<string, ConfigValue> {
  const out: Record<string, ConfigValue> = {};
  for (const f of type.fields) {
    const raw = (values[f.key] ?? '').trim();
    if (f.kind === 'bool') {
      out[f.key] = raw === 'true';
    } else if (raw === '') {
      continue;
    } else if (f.kind === 'int') {
      const n = Number(raw);
      if (Number.isInteger(n)) out[f.key] = n;
    } else {
      out[f.key] = raw;
    }
  }
  return out;
}

/** Labels of required fields that are empty or not a valid integer. */
export function invalidFields(type: ConnectorType, values: FieldValues): string[] {
  const bad: string[] = [];
  for (const f of type.fields) {
    const raw = (values[f.key] ?? '').trim();
    if (f.required && f.kind !== 'bool' && raw === '') bad.push(f.label);
    else if (f.kind === 'int' && raw !== '' && !Number.isInteger(Number(raw))) bad.push(f.label);
  }
  return bad;
}

/** Secrets whose type the connector type accepts. */
export function compatibleSecrets(
  type: ConnectorType,
  secrets: readonly SecretMeta[],
): SecretMeta[] {
  return secrets.filter((s) => type.secret_types.includes(s.type));
}

/** Whether the connector type takes credentials at all. */
export function needsSecret(type: ConnectorType): boolean {
  return type.secret_types.length > 0;
}

const STATUS_LABEL: Record<ConnectorStatus, string> = {
  disabled: 'Disabled',
  pending: 'Pending',
  ok: 'OK',
  error: 'Error',
};

export function statusLabel(s: ConnectorStatus): string {
  return STATUS_LABEL[s] ?? s;
}

/** Tailwind classes for the status badge. */
export function statusClass(s: ConnectorStatus): string {
  switch (s) {
    case 'ok':
      return 'bg-green-600/15 text-green-700 dark:text-green-400';
    case 'error':
      return 'bg-red-600/15 text-red-700 dark:text-red-400';
    case 'pending':
      return 'bg-amber-500/15 text-amber-700 dark:text-amber-400';
    default:
      return 'bg-surface-2 text-muted';
  }
}

/** Human readable duration, e.g. 850 ms, 2.4 s. */
export function fmtDuration(ms: number): string {
  if (!ms || ms < 0) return '-';
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}

/** Secret value for a new credential; key pairs are stored as a JSON object. */
export function secretValue(type: string, a: string, b: string): string {
  return type === 'api_key_pair' ? JSON.stringify({ key: a, secret: b }) : a;
}

export const SECRET_TYPE_LABEL: Record<string, string> = {
  api_key_pair: 'API key + secret',
  api_token: 'API token',
  token: 'Token',
  password: 'Password',
  ssh_key: 'SSH key',
};
