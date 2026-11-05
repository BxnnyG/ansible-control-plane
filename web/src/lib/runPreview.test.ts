import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { RunPreview } from './api';
import { createPreviewScheduler, previewSummary, type PreviewState } from './runPreview';

const pv = (n: number): RunPreview => ({
  host_count: n,
  hosts: ['h'],
  truncated: false,
  all_in_scope: true,
});

describe('createPreviewScheduler', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('debounces bursts into one load', async () => {
    const load = vi.fn().mockResolvedValue(pv(2));
    const states: PreviewState[] = [];
    const s = createPreviewScheduler(load, (x) => states.push(x), String, 100);
    s.request();
    s.request();
    s.request();
    await vi.advanceTimersByTimeAsync(100);
    expect(load).toHaveBeenCalledTimes(1);
    expect(states.at(-1)).toEqual({ kind: 'ok', preview: pv(2) });
  });

  it('drops stale responses', async () => {
    let resolveFirst!: (p: RunPreview) => void;
    const load = vi
      .fn()
      .mockImplementationOnce(() => new Promise<RunPreview>((r) => (resolveFirst = r)))
      .mockResolvedValueOnce(pv(5));
    const states: PreviewState[] = [];
    const s = createPreviewScheduler(load, (x) => states.push(x), String, 100);
    s.request();
    await vi.advanceTimersByTimeAsync(100);
    s.request();
    await vi.advanceTimersByTimeAsync(100);
    resolveFirst(pv(1));
    await vi.advanceTimersByTimeAsync(0);
    expect(states.at(-1)).toEqual({ kind: 'ok', preview: pv(5) });
  });

  it('reports errors and supports cancel', async () => {
    const load = vi.fn().mockRejectedValue(new Error('boom'));
    const states: PreviewState[] = [];
    const s = createPreviewScheduler(
      load,
      (x) => states.push(x),
      (e) => (e as Error).message,
      10,
    );
    s.request();
    await vi.advanceTimersByTimeAsync(10);
    expect(states.at(-1)).toEqual({ kind: 'error', message: 'boom' });
    s.request();
    s.cancel();
    await vi.advanceTimersByTimeAsync(50);
    expect(load).toHaveBeenCalledTimes(1);
  });
});

describe('previewSummary', () => {
  it('pluralizes and notes truncation', () => {
    expect(previewSummary(pv(1))).toBe('1 host');
    expect(previewSummary(pv(3))).toBe('3 hosts');
    expect(previewSummary({ ...pv(120), hosts: new Array(50).fill('h'), truncated: true })).toBe(
      '120 hosts (first 50 shown)',
    );
  });
});
