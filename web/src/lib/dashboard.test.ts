import { describe, expect, it } from 'vitest';
import type { Connector, FleetHost, Run } from './api';
import {
  connectorHealth,
  failingTemplates,
  fleetStats,
  recentOutcomes,
  relativeTime,
  runStats,
} from './dashboard';

const NOW = Date.parse('2026-01-02T12:00:00Z');

function run(over: Partial<Run>): Run {
  return {
    id: 'r',
    template_id: 't1',
    template_name: 'T1',
    status: 'succeeded',
    created_at: '2026-01-02T11:00:00Z',
    ...over,
  } as Run;
}

describe('runStats', () => {
  it('counts only the last 24 hours, running and queued regardless of age', () => {
    const s = runStats(
      [
        run({}),
        run({ status: 'failed' }),
        run({ status: 'lost' }),
        run({ status: 'running', created_at: '2025-12-01T00:00:00Z' }),
        run({ status: 'succeeded', created_at: '2025-12-01T00:00:00Z' }),
        run({ status: 'queued' }),
      ],
      NOW,
    );
    expect(s).toEqual({ total: 4, ok: 1, failed: 2, running: 1, queued: 1 });
  });
});

describe('recentOutcomes', () => {
  it('returns finished runs oldest first', () => {
    const o = recentOutcomes([
      run({ status: 'failed' }),
      run({ status: 'running' }),
      run({ status: 'succeeded' }),
    ]);
    expect(o).toEqual([true, false]);
  });
});

describe('failingTemplates', () => {
  it('groups failures per template, worst first', () => {
    const f = failingTemplates([
      run({ status: 'failed', template_id: 'a', template_name: 'A', id: '1' }),
      run({ status: 'failed', template_id: 'b', template_name: 'B', id: '2' }),
      run({ status: 'lost', template_id: 'b', template_name: 'B', id: '3' }),
      run({ status: 'succeeded' }),
    ]);
    expect(f.map((x) => [x.name, x.failures])).toEqual([
      ['B', 2],
      ['A', 1],
    ]);
  });
});

describe('fleetStats', () => {
  it('counts online, drifted and unmanaged hosts', () => {
    const hosts = [
      { online: 'online', drift: null, managed: true },
      { online: 'offline', drift: { changed: 2 }, managed: false },
    ] as unknown as FleetHost[];
    expect(fleetStats(hosts)).toEqual({ total: 2, online: 1, drift: 1, unmanaged: 1 });
  });
});

describe('connectorHealth', () => {
  it('buckets by status and sums warnings', () => {
    const items = [
      { enabled: true, status: 'ok', warnings: ['w'], counts: { warnings: ['x', 'y'] } },
      { enabled: true, status: 'error' },
      { enabled: false, status: 'ok' },
      { enabled: true, status: 'pending' },
    ] as unknown as Connector[];
    expect(connectorHealth(items)).toEqual({
      ok: 1,
      error: 1,
      pending: 1,
      disabled: 1,
      warnings: 3,
    });
  });
});

describe('relativeTime', () => {
  it('formats minutes, hours and days', () => {
    expect(relativeTime('2026-01-02T11:59:50Z', NOW)).toBe('just now');
    expect(relativeTime('2026-01-02T11:30:00Z', NOW)).toBe('30 min ago');
    expect(relativeTime('2026-01-02T09:00:00Z', NOW)).toBe('3 h ago');
    expect(relativeTime('2025-12-30T12:00:00Z', NOW)).toBe('3 d ago');
    expect(relativeTime(null, NOW)).toBe('-');
  });
});
