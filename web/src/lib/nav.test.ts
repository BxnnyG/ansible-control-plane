import { describe, expect, it } from 'vitest';
import { NAV_ITEMS, filterNav, groupNav, isCurrent, visibleNav } from './nav';

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

describe('isCurrent', () => {
  const [dash, runs] = [NAV_ITEMS[0], NAV_ITEMS.find((i) => i.label === 'Runs')!];
  it('matches the dashboard only on the root path', () => {
    expect(isCurrent(dash, '/')).toBe(true);
    expect(isCurrent(dash, '/runs')).toBe(false);
  });
  it('matches nested paths without prefix collisions', () => {
    expect(isCurrent(runs, '/runs/abc')).toBe(true);
    expect(isCurrent(runs, '/runsx')).toBe(false);
  });
});

describe('groupNav', () => {
  it('groups by section in order and keeps every entry', () => {
    const g = groupNav(NAV_ITEMS);
    expect(g.map((x) => x.section)).toEqual(['Operate', 'Configure', 'Security', '']);
    expect(g.flatMap((x) => x.items)).toHaveLength(NAV_ITEMS.length);
  });
});
