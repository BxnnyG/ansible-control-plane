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
