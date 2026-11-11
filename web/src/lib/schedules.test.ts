import { describe, expect, it } from 'vitest';
import {
  emptyScheduleForm,
  isValidTimezone,
  toScheduleInput,
  validateCron,
  validateSchedule,
} from './schedules';

describe('validateCron', () => {
  it('accepts standard expressions and macros', () => {
    for (const e of ['0 3 * * *', '*/15 0-6 1,15 jan-mar mon-fri', '@daily', '@HOURLY']) {
      expect(validateCron(e)).toBeNull();
    }
  });
  it('rejects bad shapes', () => {
    expect(validateCron('')).toMatch(/required/);
    expect(validateCron('* * * *')).toMatch(/five fields/);
    expect(validateCron('@sometimes')).toMatch(/macro/);
    expect(validateCron('0 3 * * $')).toMatch(/invalid/);
  });
});

describe('validateSchedule', () => {
  it('reports per-field errors', () => {
    const f = { ...emptyScheduleForm('UTC'), name: ' ', cron: 'x', timezone: 'Mars/Base' };
    expect(Object.keys(validateSchedule(f)).sort()).toEqual(['cron', 'name', 'timezone']);
  });
  it('accepts a valid form and trims on submit', () => {
    const f = { ...emptyScheduleForm('Europe/Berlin'), name: ' nightly ' };
    expect(validateSchedule(f)).toEqual({});
    expect(toScheduleInput(f)).toMatchObject({
      name: 'nightly',
      timezone: 'Europe/Berlin',
      missed_policy: 'skip',
    });
  });
  it('rejects Local as the server does', () => {
    expect(isValidTimezone('Local')).toBe(false);
    expect(isValidTimezone('UTC')).toBe(true);
  });
});

import { CRON_PRESETS, presetFor, timeZoneOptions } from './schedules';

describe('cron presets', () => {
  it('are valid cron expressions', () => {
    for (const p of CRON_PRESETS) expect(validateCron(p.cron)).toBeNull();
  });
  it('detects the active preset', () => {
    expect(presetFor('0  3 * * *')).toBe('daily');
    expect(presetFor('5 3 * * *')).toBe('');
  });
});

describe('timeZoneOptions', () => {
  it('always offers UTC and the current zone, sorted and unique', () => {
    const z = timeZoneOptions('Mars/Olympus');
    expect(z).toContain('UTC');
    expect(z).toContain('Mars/Olympus');
    expect(new Set(z).size).toBe(z.length);
    expect([...z].sort()).toEqual(z);
  });
});
