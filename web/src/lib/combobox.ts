/** Pure logic of the searchable combobox (filtering, grouping, keyboard navigation). */

export interface ComboOption {
  value: string;
  label: string;
  /** Optional group heading (for playbooks: the directory). */
  group?: string;
}

export interface ComboGroup {
  group: string;
  options: ComboOption[];
}

/** Case-insensitive match where every whitespace separated term must occur in the label. */
export function filterOptions(options: readonly ComboOption[], query: string): ComboOption[] {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return [...options];
  return options.filter((o) => {
    const hay = o.label.toLowerCase();
    return terms.every((t) => hay.includes(t));
  });
}

/** Groups by `group` (first appearance order); ungrouped options come first under ''. */
export function groupOptions(options: readonly ComboOption[]): ComboGroup[] {
  const map = new Map<string, ComboOption[]>();
  for (const o of options) {
    const key = o.group ?? '';
    const list = map.get(key);
    if (list) list.push(o);
    else map.set(key, [o]);
  }
  return [...map.entries()]
    .sort(([a], [b]) => (a === '' ? -1 : b === '' ? 1 : a.localeCompare(b)))
    .map(([group, opts]) => ({ group, options: opts }));
}

/** Playbook options grouped by directory ('.' for the repository root). */
export function playbookOptions(paths: readonly string[]): ComboOption[] {
  return paths.map((p) => {
    const i = p.lastIndexOf('/');
    return { value: p, label: p, group: i < 0 ? '.' : p.slice(0, i) };
  });
}

/** Flat display order: the order in which grouped options are rendered and navigated. */
export function flatOrder(options: readonly ComboOption[]): ComboOption[] {
  return groupOptions(options).flatMap((g) => g.options);
}

export type NavKey = 'ArrowDown' | 'ArrowUp' | 'Home' | 'End';

/** Next active index for a navigation key; wraps around, -1 when the list is empty. */
export function moveActive(current: number, count: number, key: NavKey): number {
  if (count <= 0) return -1;
  switch (key) {
    case 'Home':
      return 0;
    case 'End':
      return count - 1;
    case 'ArrowDown':
      return current < 0 || current >= count - 1 ? 0 : current + 1;
    case 'ArrowUp':
      return current <= 0 ? count - 1 : current - 1;
  }
}
