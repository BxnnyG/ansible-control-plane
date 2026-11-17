import { describe, expect, it } from 'vitest';
import type { ReachabilityResult } from './api';
import {
  cellView,
  draftError,
  draftToInput,
  emptyDraft,
  indexResults,
  intervalLabel,
  isStale,
  runHref,
  validInterval,
} from './reachability';

const NOW = Date.parse('2026-01-02T12:00:00Z');

function res(over: Partial<ReachabilityResult>): ReachabilityResult {
  return {
    source_id: 's',
    target_id: 't',
    status: 'ok',
    latency_ms: 12,
    detail: '',
    run_id: 'r1',
    checked_at: '2026-01-02T11:59:00Z',
    ...over,
  };
}

describe('cellView', () => {
  it('maps statuses to distinct labels, icons and tones', () => {
    const ok = cellView(res({}), 300, NOW);
    const bad = cellView(res({ status: 'failed', detail: 'timeout' }), 300, NOW);
    const grey = cellView(res({ status: 'probe_unreachable' }), 300, NOW);
    expect(ok.tone).toBe('tone-ok');
    expect(bad.tone).toBe('tone-bad');
    expect(grey.tone).toBe('tone-neutral');
    expect(new Set([ok.icon, bad.icon, grey.icon]).size).toBe(3);
    expect(grey.label).toContain('unreachable');
    expect(ok.title).toContain('12 ms');
    expect(bad.title).toContain('timeout');
  });

  it('is empty when never run', () => {
    const v = cellView(undefined, 300, NOW);
    expect(v.state).toBe('none');
    expect(v.icon).toBe('');
    expect(v.stale).toBe(false);
  });

  it('omits latency when null', () => {
    expect(cellView(res({ latency_ms: null }), 300, NOW).title).not.toContain('Latency');
  });

  it('marks stale results', () => {
    const v = cellView(res({ checked_at: '2026-01-02T11:00:00Z' }), 300, NOW);
    expect(v.stale).toBe(true);
    expect(v.title).toContain('Stale');
  });
});

describe('isStale', () => {
  it('uses twice the interval and ignores a disabled scheduler', () => {
    expect(isStale('2026-01-02T11:49:00Z', 300, NOW)).toBe(true);
    expect(isStale('2026-01-02T11:55:00Z', 300, NOW)).toBe(false);
    expect(isStale('2025-01-01T00:00:00Z', 0, NOW)).toBe(false);
  });
});

describe('helpers', () => {
  it('indexes results and builds run links', () => {
    expect(indexResults([res({})]).get('s:t')?.run_id).toBe('r1');
    expect(runHref(res({}))).toBe('/runs/r1');
    expect(runHref(res({ run_id: null }))).toBeNull();
    expect(runHref(undefined)).toBeNull();
  });

  it('formats and validates intervals', () => {
    expect(intervalLabel(0)).toBe('Off');
    expect(intervalLabel(300)).toBe('5 min');
    expect(intervalLabel(7200)).toBe('2 h');
    expect(validInterval(0)).toBe(true);
    expect(validInterval(30)).toBe(false);
    expect(validInterval(60)).toBe(true);
    expect(validInterval(86401)).toBe(false);
  });

  it('validates target drafts', () => {
    const d = emptyDraft();
    expect(draftError(d)).toMatch(/Name/);
    d.name = 'gw';
    d.address = '192.0.2.1';
    expect(draftError(d)).toBeNull();
    d.check = 'tcp';
    expect(draftError(d)).toMatch(/port/);
    d.port = '443';
    expect(draftError(d)).toBeNull();
    expect(draftToInput(d).port).toBe(443);
    d.check = 'dns';
    expect(draftError(d)).toMatch(/resolve/);
    d.dns_name = 'example.test';
    expect(draftToInput(d)).toMatchObject({ port: null, dns_name: 'example.test' });
  });
});
