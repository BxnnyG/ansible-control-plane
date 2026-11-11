import { describe, expect, it } from 'vitest';
import { shouldRefresh } from './polling';

describe('shouldRefresh', () => {
  it('never refreshes a hidden tab', () => expect(shouldRefresh(true, 0, 1e9, 1000)).toBe(false));
  it('waits for the minimum age', () => {
    expect(shouldRefresh(false, 1000, 1500, 1000)).toBe(false);
    expect(shouldRefresh(false, 1000, 2000, 1000)).toBe(true);
  });
});
