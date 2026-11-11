import { describe, expect, it } from 'vitest';
import { isDirty, snapshotOf } from './dirty';

describe('isDirty', () => {
  const base = snapshotOf({ name: 'a', ids: ['x'] });
  it('is clean for an identical snapshot', () => {
    expect(isDirty(base, snapshotOf({ name: 'a', ids: ['x'] }), false)).toBe(false);
  });
  it('detects changes, including nested ones', () => {
    expect(isDirty(base, snapshotOf({ name: 'b', ids: ['x'] }), false)).toBe(true);
    expect(isDirty(base, snapshotOf({ name: 'a', ids: ['x', 'y'] }), false)).toBe(true);
  });
  it('is never dirty after a successful save', () => {
    expect(isDirty(base, snapshotOf({ name: 'b', ids: [] }), true)).toBe(false);
  });
});
