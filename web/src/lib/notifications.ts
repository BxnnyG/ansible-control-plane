import type {
  NotificationChannel,
  NotificationDelivery,
  NotificationRule,
  SecretMeta,
} from './api';

export type ChannelType = NotificationChannel['type'];
export type EventKind = NotificationRule['events'][number];

export const CHANNEL_TYPES: readonly { type: ChannelType; label: string }[] = [
  { type: 'webhook', label: 'Webhook' },
  { type: 'matrix', label: 'Matrix' },
  { type: 'smtp', label: 'E-mail (SMTP)' },
  { type: 'gotify', label: 'Gotify' },
  { type: 'ntfy', label: 'ntfy' },
];

export const EVENT_KINDS: readonly { kind: EventKind; label: string }[] = [
  { kind: 'run.failed', label: 'Run failed' },
  { kind: 'run.recovered', label: 'Run recovered' },
  { kind: 'run.drift', label: 'Drift detected' },
  { kind: 'run.finished', label: 'Every finished run' },
];

/** Secret types a channel credential may use. */
export const CHANNEL_SECRET_TYPES: readonly string[] = ['token', 'api_token', 'password'];

export function typeLabel(t: string): string {
  return CHANNEL_TYPES.find((x) => x.type === t)?.label ?? t;
}

export function eventLabel(k: string): string {
  return EVENT_KINDS.find((x) => x.kind === k)?.label ?? k;
}

/** Text-based form state covering the fields of every channel type. */
export interface ChannelFields {
  url: string;
  auth: 'hmac' | 'bearer';
  ntfyAuth: 'token' | 'basic';
  homeserver: string;
  roomId: string;
  host: string;
  port: string;
  security: 'starttls' | 'tls' | 'none';
  insecure: boolean;
  username: string;
  from: string;
  to: string;
  topic: string;
  priority: string;
}

export function emptyFields(): ChannelFields {
  return {
    url: '',
    auth: 'hmac',
    ntfyAuth: 'token',
    homeserver: '',
    roomId: '',
    host: '',
    port: '',
    security: 'starttls',
    insecure: false,
    username: '',
    from: '',
    to: '',
    topic: '',
    priority: '',
  };
}

const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const num = (v: unknown): string => (typeof v === 'number' ? String(v) : '');

/** Form fields for an existing channel config (unknown keys are ignored). */
export function fieldsFromConfig(
  type: ChannelType,
  config: Record<string, unknown> | undefined,
): ChannelFields {
  const f = emptyFields();
  const c = config ?? {};
  f.url = str(c.url);
  if (type === 'webhook' && c.auth === 'bearer') f.auth = 'bearer';
  if (type === 'ntfy' && c.auth === 'basic') f.ntfyAuth = 'basic';
  f.homeserver = str(c.homeserver);
  f.roomId = str(c.room_id);
  f.host = str(c.host);
  f.port = num(c.port);
  if (c.security === 'tls' || c.security === 'none') f.security = c.security;
  f.insecure = c.insecure === true;
  f.username = str(c.username);
  f.from = str(c.from);
  f.to = Array.isArray(c.to) ? c.to.filter((x) => typeof x === 'string').join(', ') : '';
  f.topic = str(c.topic);
  f.priority = num(c.priority);
  return f;
}

/** Splits a comma/whitespace/semicolon separated recipient list. */
export function parseRecipients(s: string): string[] {
  return s
    .split(/[\s,;]+/)
    .map((x) => x.trim())
    .filter(Boolean);
}

function intOrUndef(s: string): number | undefined {
  const t = s.trim();
  if (t === '') return undefined;
  const n = Number(t);
  return Number.isInteger(n) ? n : undefined;
}

/** Builds the type specific config; empty optional fields are omitted. */
export function buildChannelConfig(type: ChannelType, f: ChannelFields): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const set = (k: string, v: unknown) => {
    if (v !== undefined && v !== '') out[k] = v;
  };
  switch (type) {
    case 'webhook':
      set('url', f.url.trim());
      set('auth', f.auth);
      break;
    case 'matrix':
      set('homeserver', f.homeserver.trim());
      set('room_id', f.roomId.trim());
      break;
    case 'smtp':
      set('host', f.host.trim());
      set('port', intOrUndef(f.port));
      set('security', f.security);
      if (f.insecure) out.insecure = true;
      set('username', f.username.trim());
      set('from', f.from.trim());
      out.to = parseRecipients(f.to);
      break;
    case 'gotify':
      set('url', f.url.trim());
      set('priority', intOrUndef(f.priority));
      break;
    case 'ntfy':
      set('url', f.url.trim());
      set('topic', f.topic.trim());
      if (f.ntfyAuth === 'basic') {
        out.auth = 'basic';
        set('username', f.username.trim());
      }
      set('priority', intOrUndef(f.priority));
      break;
  }
  return out;
}

function isHttpUrl(s: string): boolean {
  try {
    const u = new URL(s.trim());
    return (u.protocol === 'http:' || u.protocol === 'https:') && u.host !== '' && !u.username;
  } catch {
    return false;
  }
}

function isEmail(s: string): boolean {
  return s.includes('@') && !/[\r\n<>]/.test(s);
}

/** Client side mirror of the server validation; returns the first problem or null. */
export function validateChannel(
  type: ChannelType,
  name: string,
  f: ChannelFields,
  hasSecret: boolean,
): string | null {
  if (!name.trim()) return 'Name is required.';
  switch (type) {
    case 'webhook':
      if (!isHttpUrl(f.url)) return 'URL must be an http(s) URL.';
      if (f.auth === 'bearer' && !hasSecret) return 'Bearer auth requires a secret.';
      return null;
    case 'matrix':
      if (!isHttpUrl(f.homeserver)) return 'Homeserver must be an http(s) URL.';
      if (!f.roomId.trim().startsWith('!') || !f.roomId.includes(':'))
        return 'Room ID must look like !room:server.';
      if (!hasSecret) return 'Matrix requires a secret holding the access token.';
      return null;
    case 'smtp': {
      const host = f.host.trim();
      if (!host || /[/\s]/.test(host)) return 'Host is required.';
      if (f.port.trim() !== '') {
        const p = Number(f.port);
        if (!Number.isInteger(p) || p < 0 || p > 65535) return 'Port is out of range.';
      }
      if (f.security === 'none' && !f.insecure)
        return 'Security "none" requires the insecure option.';
      if (!f.from.trim() || !isEmail(f.from.trim())) return 'A valid sender address is required.';
      const to = parseRecipients(f.to);
      if (to.length === 0) return 'At least one recipient is required.';
      if (!to.every(isEmail)) return 'Invalid recipient address.';
      if (hasSecret && !f.username.trim())
        return 'Username is required when a password secret is set.';
      return null;
    }
    case 'gotify':
      if (!isHttpUrl(f.url)) return 'URL must be an http(s) URL.';
      if (!hasSecret) return 'Gotify requires a secret holding the application token.';
      return null;
    case 'ntfy':
      if (!isHttpUrl(f.url)) return 'URL must be an http(s) URL.';
      if (!f.topic.trim() || /[/?# ]/.test(f.topic.trim()))
        return 'Topic must be a single path segment.';
      if (f.ntfyAuth === 'basic' && hasSecret && !f.username.trim())
        return 'Username is required for basic authentication.';
      return null;
  }
}

/** Whether the type can use a credential at all (all do; required varies). */
export function secretRequired(type: ChannelType, f: ChannelFields): boolean {
  return type === 'matrix' || type === 'gotify' || (type === 'webhook' && f.auth === 'bearer');
}

export function usableSecrets(secrets: readonly SecretMeta[]): SecretMeta[] {
  return secrets.filter((s) => CHANNEL_SECRET_TYPES.includes(s.type));
}

export function deliveryStatusClass(s: NotificationDelivery['status']): string {
  switch (s) {
    case 'sent':
      return 'bg-green-600/15 text-green-700 dark:text-green-400';
    case 'failed':
      return 'bg-red-600/15 text-red-700 dark:text-red-400';
    default:
      return 'bg-amber-600/15 text-amber-700 dark:text-amber-400';
  }
}

/** Toggles an event kind, keeping the canonical order. */
export function toggleEvent(events: readonly EventKind[], kind: EventKind): EventKind[] {
  const set = new Set(events);
  if (set.has(kind)) set.delete(kind);
  else set.add(kind);
  return EVENT_KINDS.map((e) => e.kind).filter((k) => set.has(k));
}

/** Channels that no rule references (they never send anything automatically). */
export function channelsWithoutRules(
  channels: readonly NotificationChannel[],
  rules: readonly NotificationRule[],
): NotificationChannel[] {
  const used = new Set(rules.map((r) => r.channel_id));
  return channels.filter((c) => !used.has(c.id));
}
