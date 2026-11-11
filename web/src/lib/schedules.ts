import type { MissedPolicy, ScheduleInput } from './api';

export interface ScheduleForm {
  name: string;
  cron: string;
  timezone: string;
  enabled: boolean;
  missedPolicy: MissedPolicy;
}

const MACROS = new Set([
  '@hourly',
  '@daily',
  '@weekly',
  '@monthly',
  '@yearly',
  '@annually',
  '@midnight',
]);
const FIELD = /^[A-Za-z0-9*,/\-?]+$/;

/** Browser time zone (IANA name); UTC when unavailable. */
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

export function emptyScheduleForm(tz: string = browserTimezone()): ScheduleForm {
  return { name: '', cron: '0 3 * * *', timezone: tz, enabled: true, missedPolicy: 'skip' };
}

/** Cheap client-side shape check; the server stays authoritative (and powers the preview). */
export function validateCron(expr: string): string | null {
  const e = expr.trim();
  if (!e) return 'Cron expression is required.';
  if (e.startsWith('@')) return MACROS.has(e.toLowerCase()) ? null : `Unknown macro ${e}.`;
  const fields = e.split(/\s+/);
  if (fields.length !== 5) return 'Use five fields: minute hour day-of-month month day-of-week.';
  if (!fields.every((f) => FIELD.test(f))) return 'Cron fields contain invalid characters.';
  return null;
}

export function isValidTimezone(tz: string): boolean {
  if (!tz.trim() || tz.trim() === 'Local') return false;
  try {
    new Intl.DateTimeFormat('en', { timeZone: tz.trim() });
    return true;
  } catch {
    return false;
  }
}

/** Field errors keyed by form field; empty when valid. */
export function validateSchedule(
  f: ScheduleForm,
): Partial<Record<'name' | 'cron' | 'timezone', string>> {
  const errs: Partial<Record<'name' | 'cron' | 'timezone', string>> = {};
  if (!f.name.trim()) errs.name = 'Name is required.';
  const cron = validateCron(f.cron);
  if (cron) errs.cron = cron;
  if (!isValidTimezone(f.timezone))
    errs.timezone = 'Unknown time zone (use an IANA name such as Europe/Berlin).';
  return errs;
}

export function toScheduleInput(f: ScheduleForm): ScheduleInput {
  return {
    name: f.name.trim(),
    cron: f.cron.trim(),
    timezone: f.timezone.trim(),
    enabled: f.enabled,
    missed_policy: f.missedPolicy,
  };
}

export interface CronPreset {
  id: string;
  label: string;
  cron: string;
}

/** Quick picks for the cron field; the expression stays freely editable. */
export const CRON_PRESETS: readonly CronPreset[] = [
  { id: 'hourly', label: 'Hourly', cron: '0 * * * *' },
  { id: 'daily', label: 'Daily at 03:00', cron: '0 3 * * *' },
  { id: 'weekly', label: 'Weekly (Monday 03:00)', cron: '0 3 * * 1' },
];

/** Id of the preset matching `cron` (whitespace-insensitive), or '' for a custom expression. */
export function presetFor(cron: string): string {
  const norm = cron.trim().split(/\s+/).join(' ');
  return CRON_PRESETS.find((p) => p.cron === norm)?.id ?? '';
}

/** IANA time zones for the datalist: Intl list when supported, otherwise a short fallback. */
export function timeZoneOptions(current: string = browserTimezone()): string[] {
  let zones: string[] = [];
  try {
    const fn = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] })
      .supportedValuesOf;
    zones = fn ? fn('timeZone') : [];
  } catch {
    zones = [];
  }
  if (zones.length === 0) zones = ['UTC', 'Europe/Berlin', 'Europe/London', 'America/New_York'];
  const set = new Set(zones);
  set.add('UTC');
  if (current) set.add(current);
  return [...set].sort();
}
