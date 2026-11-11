import { describe, expect, it } from 'vitest';
import {
  filterOptions,
  flatOrder,
  groupOptions,
  moveActive,
  playbookOptions,
  type ComboOption,
} from './combobox';

const opts: ComboOption[] = playbookOptions([
  'site.yml',
  'roles/web/deploy.yml',
  'roles/db/backup.yml',
  'maintenance/patch.yml',
]);

describe('filterOptions', () => {
  it('returns everything for an empty query', () => {
    expect(filterOptions(opts, '  ')).toHaveLength(4);
  });
  it('matches case-insensitively and requires all terms', () => {
    expect(filterOptions(opts, 'ROLES yml').map((o) => o.value)).toEqual([
      'roles/web/deploy.yml',
      'roles/db/backup.yml',
    ]);
    expect(filterOptions(opts, 'patch db')).toEqual([]);
  });
});

describe('grouping', () => {
  it('groups playbooks by directory with the root first', () => {
    const g = groupOptions(opts);
    expect(g.map((x) => x.group)).toEqual(['.', 'maintenance', 'roles/db', 'roles/web']);
    expect(flatOrder(opts).map((o) => o.value)[0]).toBe('site.yml');
  });
});

describe('moveActive (keyboard)', () => {
  it('moves down and up with wrap-around', () => {
    expect(moveActive(-1, 3, 'ArrowDown')).toBe(0);
    expect(moveActive(2, 3, 'ArrowDown')).toBe(0);
    expect(moveActive(0, 3, 'ArrowUp')).toBe(2);
    expect(moveActive(1, 3, 'ArrowUp')).toBe(0);
  });
  it('supports Home and End and empty lists', () => {
    expect(moveActive(1, 3, 'Home')).toBe(0);
    expect(moveActive(1, 3, 'End')).toBe(2);
    expect(moveActive(0, 0, 'ArrowDown')).toBe(-1);
  });
});
