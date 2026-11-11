import { describe, expect, it } from 'vitest';
import type { Run } from './api';
import {
  applyRunUpdate,
  buildRunQuery,
  canCancelRun,
  collectUsers,
  formatDuration,
  isFinal,
  isUuid,
  matchesFilter,
  userOptions,
  triggeredByLabel,
} from './runs';

function run(id: string, created: string, over: Partial<Run> = {}): Run {
  return {
    id,
    template_id: 't1',
    template_name: 'T',
    project_id: null,
    project_revision: 'abc',
    status: 'running',
    trigger: 'manual',
    triggered_by: 'u1',
    triggered_by_name: 'User One',
    schedule_id: null,
    overrides: null,
    remediation: null,
    worker_id: '',
    params: { hosts: ['h1'] } as Run['params'],
    created_at: created,
    started_at: null,
    finished_at: null,
    cancel_requested_at: null,
    exit_code: null,
    hosts_ok: 0,
    hosts_changed: 0,
    hosts_failed: 0,
    hosts_unreachable: 0,
    ...over,
  };
}

describe('buildRunQuery', () => {
  it('drops empty values and trims', () => {
    expect(buildRunQuery({ status: 'failed', host: ' web1 ', trigger: '', limit: 25 })).toBe(
      'status=failed&host=web1&limit=25',
    );
    expect(buildRunQuery({})).toBe('');
  });
  it('encodes cursors', () => {
    expect(buildRunQuery({ cursor: 'a b+c' })).toBe('cursor=a+b%2Bc');
  });
});

describe('formatDuration', () => {
  it('formats ranges', () => {
    const s = '2026-01-01T00:00:00Z';
    expect(formatDuration(s, '2026-01-01T00:00:07Z')).toBe('7s');
    expect(formatDuration(s, '2026-01-01T00:01:05Z')).toBe('1m 05s');
    expect(formatDuration(s, '2026-01-01T01:02:03Z')).toBe('1h 02m 03s');
    expect(formatDuration(null, null)).toBe('-');
    expect(formatDuration(s, null, Date.parse(s) + 3000)).toBe('3s');
  });
});

describe('live updates', () => {
  it('replaces known runs and inserts new ones by time', () => {
    const list = [run('b', '2026-01-02T00:00:00Z'), run('a', '2026-01-01T00:00:00Z')];
    const upd = applyRunUpdate(list, run('a', '2026-01-01T00:00:00Z', { status: 'failed' }), {});
    expect(upd[1].status).toBe('failed');
    const ins = applyRunUpdate(list, run('c', '2026-01-03T00:00:00Z'), {});
    expect(ins.map((r) => r.id)).toEqual(['c', 'b', 'a']);
  });
  it('drops runs that stop matching and ignores non-matching new ones', () => {
    const list = [run('a', '2026-01-01T00:00:00Z')];
    expect(
      applyRunUpdate(list, run('a', '2026-01-01T00:00:00Z', { status: 'failed' }), {
        status: 'running',
      }),
    ).toEqual([]);
    expect(
      applyRunUpdate(list, run('z', '2026-01-05T00:00:00Z', { trigger: 'api' }), {
        trigger: 'manual',
      }),
    ).toBe(list);
  });
  it('matches host filters on the effective host set', () => {
    expect(matchesFilter(run('a', 'x'), { host: 'h1' })).toBe(true);
    expect(matchesFilter(run('a', 'x'), { host: 'h2' })).toBe(false);
  });
});

describe('helpers', () => {
  it('detects final states and uuids', () => {
    expect(isFinal('running')).toBe(false);
    expect(isFinal('lost')).toBe(true);
    expect(isUuid('7d444840-9dc0-11d1-b245-5ffdce74fad2')).toBe(true);
    expect(isUuid('nope')).toBe(false);
  });
});

describe('triggeredByLabel', () => {
  it('prefers you, then the name, never the raw uuid', () => {
    expect(triggeredByLabel(run('x', '2026-01-01T00:00:00Z', {}), 'u1')).toBe('you');
    expect(triggeredByLabel(run('x', '2026-01-01T00:00:00Z', {}), 'u2')).toBe('User One');
    expect(
      triggeredByLabel(run('x', '2026-01-01T00:00:00Z', { triggered_by_name: null }), 'u2'),
    ).toBe('unknown user');
    expect(
      triggeredByLabel(
        run('x', '2026-01-01T00:00:00Z', { triggered_by: null, trigger: 'schedule' }),
        'u2',
      ),
    ).toBe('-');
    expect(triggeredByLabel(run('x', '2026-01-01T00:00:00Z', { triggered_by: null }), 'u2')).toBe(
      'unknown',
    );
  });
});

describe('user filter helpers', () => {
  const a = run('1', '2026-01-01T00:00:00Z', { triggered_by: 'u1', triggered_by_name: 'Zed' });
  const b = run('2', '2026-01-01T00:00:00Z', { triggered_by: 'u2', triggered_by_name: 'Amy' });
  const sched = run('3', '2026-01-01T00:00:00Z', { triggered_by: null, triggered_by_name: null });

  it('collects users from runs and skips unattended runs', () => {
    expect(collectUsers({}, [a, b, sched])).toEqual({ u1: 'Zed', u2: 'Amy' });
    expect(collectUsers({ u9: 'Old' }, [a])).toEqual({ u9: 'Old', u1: 'Zed' });
  });
  it('lists me first, then names alphabetically, and keeps the selected id', () => {
    const known = { u1: 'Zed', u2: 'Amy' };
    expect(userOptions(known, { id: 'u1' }, '').map((o) => o.label)).toEqual(['you', 'Amy']);
    expect(userOptions(known, undefined, '').map((o) => o.label)).toEqual(['Amy', 'Zed']);
    expect(userOptions(known, undefined, 'x9')).toContainEqual({
      id: 'x9',
      label: 'selected user',
    });
  });
});

describe('canCancelRun', () => {
  it('requires permission, an active status and no pending cancel', () => {
    expect(canCancelRun(run('1', 'x', { status: 'running' }), true)).toBe(true);
    expect(canCancelRun(run('1', 'x', { status: 'queued' }), true)).toBe(true);
    expect(canCancelRun(run('1', 'x', { status: 'running' }), false)).toBe(false);
    expect(canCancelRun(run('1', 'x', { status: 'failed' }), true)).toBe(false);
    expect(
      canCancelRun(
        run('1', 'x', { status: 'running', cancel_requested_at: '2026-01-01T00:00:00Z' }),
        true,
      ),
    ).toBe(false);
  });
});
