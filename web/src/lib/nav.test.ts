import { describe, expect, it } from 'vitest';
import { NAV_ITEMS, filterNav } from './nav';

describe('filterNav', () => {
  it('returns all entries for an empty query', () => {
    expect(filterNav(NAV_ITEMS, '  ')).toHaveLength(NAV_ITEMS.length);
  });

  it('filters case-insensitively by label', () => {
    expect(filterNav(NAV_ITEMS, 'RUN').map((i) => i.label)).toEqual(['Runs']);
  });

  it('returns nothing when no entry matches', () => {
    expect(filterNav(NAV_ITEMS, 'zzz')).toEqual([]);
  });
});
