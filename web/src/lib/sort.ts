/** Client-side column sorting shared by the list pages. */

export type SortDir = 'asc' | 'desc';
export interface SortState {
  key: string;
  dir: SortDir;
}

export type SortValue = string | number | null | undefined;

/** Click on a column: new column sorts ascending, the same column toggles, a third click clears. */
export function nextSort(cur: SortState | null, key: string): SortState | null {
  if (!cur || cur.key !== key) return { key, dir: 'asc' };
  return cur.dir === 'asc' ? { key, dir: 'desc' } : null;
}

export function ariaSort(cur: SortState | null, key: string): 'ascending' | 'descending' | 'none' {
  if (!cur || cur.key !== key) return 'none';
  return cur.dir === 'asc' ? 'ascending' : 'descending';
}

function compare(a: SortValue, b: SortValue): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
}

/**
 * Returns a sorted copy (stable). Empty values (null, undefined, '') always sort last, in both
 * directions. With no state the input order is kept.
 */
export function sortRows<T>(
  rows: readonly T[],
  state: SortState | null,
  accessors: Record<string, (row: T) => SortValue>,
): T[] {
  const get = state ? accessors[state.key] : undefined;
  if (!state || !get) return [...rows];
  const sign = state.dir === 'asc' ? 1 : -1;
  const empty = (v: SortValue) => v === null || v === undefined || v === '';
  return rows
    .map((row, i) => ({ row, i, v: get(row) }))
    .sort((x, y) => {
      const ex = empty(x.v);
      const ey = empty(y.v);
      if (ex || ey) return ex === ey ? x.i - y.i : ex ? 1 : -1;
      return sign * compare(x.v, y.v) || x.i - y.i;
    })
    .map((e) => e.row);
}
