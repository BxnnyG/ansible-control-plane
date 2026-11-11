import { describe, expect, it } from 'vitest';
import { ariaSort, nextSort, sortRows } from './sort';

type R = { n: string | null; c: number };
const rows: R[] = [
  { n: 'b10', c: 2 },
  { n: null, c: 9 },
  { n: 'b2', c: 1 },
  { n: 'A', c: 3 },
];
const acc = { n: (r: R) => r.n, c: (r: R) => r.c };

describe('nextSort', () => {
  it('cycles asc, desc, none', () => {
    const a = nextSort(null, 'n');
    expect(a).toEqual({ key: 'n', dir: 'asc' });
    const d = nextSort(a, 'n');
    expect(d).toEqual({ key: 'n', dir: 'desc' });
    expect(nextSort(d, 'n')).toBeNull();
    expect(nextSort(d, 'c')).toEqual({ key: 'c', dir: 'asc' });
  });
  it('maps to aria-sort', () => {
    expect(ariaSort({ key: 'n', dir: 'desc' }, 'n')).toBe('descending');
    expect(ariaSort({ key: 'n', dir: 'desc' }, 'c')).toBe('none');
    expect(ariaSort(null, 'n')).toBe('none');
  });
});

describe('sortRows', () => {
  it('sorts naturally and case-insensitively, empty last', () => {
    expect(sortRows(rows, { key: 'n', dir: 'asc' }, acc).map((r) => r.n)).toEqual([
      'A',
      'b2',
      'b10',
      null,
    ]);
    expect(sortRows(rows, { key: 'n', dir: 'desc' }, acc).map((r) => r.n)).toEqual([
      'b10',
      'b2',
      'A',
      null,
    ]);
  });
  it('sorts numbers and does not mutate', () => {
    const copy = [...rows];
    expect(sortRows(rows, { key: 'c', dir: 'desc' }, acc).map((r) => r.c)).toEqual([9, 3, 2, 1]);
    expect(rows).toEqual(copy);
  });
  it('keeps order without state or unknown key', () => {
    expect(sortRows(rows, null, acc)).toEqual(rows);
    expect(sortRows(rows, { key: 'zz', dir: 'asc' }, acc)).toEqual(rows);
  });
});
