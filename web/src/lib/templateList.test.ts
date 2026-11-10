import { describe, expect, it } from 'vitest';
import type { Run, Schedule, Template } from './api';
import { filterTemplates, lastRunByTemplate, scheduleLabel } from './templateList';

const t = (name: string, playbook = 'site.yml', description = ''): Template =>
  ({ name, playbook, description }) as Template;
const r = (id: string, template_id: string | null): Run => ({ id, template_id }) as Run;
const s = (enabled: boolean): Schedule => ({ enabled }) as Schedule;

describe('filterTemplates', () => {
  const items = [t('Patch web', 'patch.yml'), t('Backup', 'bk.yml', 'nightly dump')];
  it('keeps all for a blank query', () => expect(filterTemplates(items, '  ')).toHaveLength(2));
  it('matches name, playbook and description case-insensitively', () => {
    expect(filterTemplates(items, 'PATCH').map((x) => x.name)).toEqual(['Patch web']);
    expect(filterTemplates(items, 'bk.yml')).toHaveLength(1);
    expect(filterTemplates(items, 'nightly')).toHaveLength(1);
    expect(filterTemplates(items, 'zzz')).toHaveLength(0);
  });
});

describe('lastRunByTemplate', () => {
  it('keeps the first (newest) run per template and skips orphans', () => {
    const m = lastRunByTemplate([r('1', 'a'), r('2', 'b'), r('3', 'a'), r('4', null)]);
    expect(m.get('a')?.id).toBe('1');
    expect(m.get('b')?.id).toBe('2');
    expect(m.size).toBe(2);
  });
});

describe('scheduleLabel', () => {
  it('summarises schedules', () => {
    expect(scheduleLabel(undefined)).toBe('-');
    expect(scheduleLabel([])).toBe('-');
    expect(scheduleLabel([s(false)])).toBe('paused');
    expect(scheduleLabel([s(true), s(false)])).toBe('scheduled');
    expect(scheduleLabel([s(true), s(true)])).toBe('2 schedules');
  });
});
