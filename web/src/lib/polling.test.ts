import { describe, expect, it } from 'vitest';
import { shouldRefresh } from './polling';

describe('shouldRefresh', () => {
  it('never refreshes a hidden tab', () => expect(shouldRefresh(true, 0, 1e9, 1000)).toBe(false));
  it('waits for the minimum age', () => {
    expect(shouldRefresh(false, 1000, 1500, 1000)).toBe(false);
    expect(shouldRefresh(false, 1000, 2000, 1000)).toBe(true);
  });
});

import { pollUntil } from './polling';

describe('pollUntil', () => {
  const opts = { intervalMs: 10, maxMs: 30, sleep: async () => {} };
  it('stops as soon as the condition is met', async () => {
    let n = 0;
    const r = await pollUntil(
      async () => ++n,
      (v) => v === 2,
      opts,
    );
    expect(r).toEqual({ met: true, value: 2 });
  });
  it('gives up after maxMs', async () => {
    let n = 0;
    const r = await pollUntil(
      async () => ++n,
      () => false,
      opts,
    );
    expect(r.met).toBe(false);
    expect(n).toBe(3);
  });
  it('ends on fetch errors', async () => {
    const r = await pollUntil(
      async () => {
        throw new Error('x');
      },
      () => true,
      opts,
    );
    expect(r).toEqual({ met: false, value: undefined });
  });
});
