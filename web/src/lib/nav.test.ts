import { describe, expect, it } from 'vitest';
import { NAV_ITEMS, filterNav, visibleNav } from './nav';

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

describe('visibleNav', () => {
  it('hides entries whose permission is missing', () => {
    const labels = visibleNav(NAV_ITEMS, () => false).map((i) => i.label);
    expect(labels).not.toContain('Fleet');
    expect(labels).not.toContain('Connectors');
    expect(labels).toContain('Runs');
  });

  it('shows entries when the permission is granted', () => {
    const labels = visibleNav(NAV_ITEMS, (p) => p === 'connector.read').map((i) => i.label);
    expect(labels).toContain('Connectors');
    expect(labels).not.toContain('Fleet');
  });
});
