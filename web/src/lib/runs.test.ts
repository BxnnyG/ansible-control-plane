import { describe, expect, it } from 'vitest';
import type { Run } from './api';
import {
  applyRunUpdate,
  buildRunQuery,
  formatDuration,
  isFinal,
  isUuid,
  matchesFilter,
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
