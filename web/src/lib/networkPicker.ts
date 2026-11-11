/** Pure selection logic of the network picker (filter, groups, tri-state, bulk actions). */

export interface PickerNetwork {
  id: string;
  cidr: string;
  description?: string | null;
  interface?: string | null;
  vlan?: number | string | null;
  connector: string;
  tracked: boolean;
}

export type Selection = Record<string, boolean>;
export type GroupState = 'all' | 'some' | 'none';

export interface NetworkGroup<T extends PickerNetwork = PickerNetwork> {
  connector: string;
  items: T[];
}

/** Case-insensitive match on CIDR, description, interface, VLAN and connector. */
export function filterNetworks<T extends PickerNetwork>(
  networks: readonly T[],
  query: string,
): T[] {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return [...networks];
  return networks.filter((n) => {
    const hay = [
      n.cidr,
      n.description,
      n.interface,
      n.vlan == null ? '' : `vlan ${n.vlan}`,
      n.connector,
    ]
      .filter(Boolean)
      .join(' ')
      .toLowerCase();
    return terms.every((t) => hay.includes(t));
  });
}

/** Groups by connector name (sorted); the order inside a group is kept. */
export function groupByConnector<T extends PickerNetwork>(
  networks: readonly T[],
): NetworkGroup<T>[] {
  const map = new Map<string, T[]>();
  for (const n of networks) {
    const list = map.get(n.connector);
    if (list) list.push(n);
    else map.set(n.connector, [n]);
  }
  return [...map.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([connector, items]) => ({ connector, items }));
}

/** Selection map with the current tracked flags. */
export function initialSelection(networks: readonly PickerNetwork[]): Selection {
  return Object.fromEntries(networks.map((n) => [n.id, n.tracked]));
}

/** Tri-state of a group checkbox. */
export function groupState(items: readonly PickerNetwork[], sel: Selection): GroupState {
  const on = items.filter((n) => sel[n.id]).length;
  if (on === 0) return 'none';
  return on === items.length ? 'all' : 'some';
}

function withAll(
  sel: Selection,
  items: readonly PickerNetwork[],
  f: (on: boolean) => boolean,
): Selection {
  const next = { ...sel };
  for (const n of items) next[n.id] = f(!!sel[n.id]);
  return next;
}

/** Sets every network of a group (or of the filtered set). */
export function setMany(sel: Selection, items: readonly PickerNetwork[], on: boolean): Selection {
  return withAll(sel, items, () => on);
}

/** Toggles a group checkbox: anything but fully selected becomes fully selected. */
export function toggleGroup(sel: Selection, items: readonly PickerNetwork[]): Selection {
  return setMany(sel, items, groupState(items, sel) !== 'all');
}

export const selectAll = (sel: Selection, filtered: readonly PickerNetwork[]): Selection =>
  setMany(sel, filtered, true);
export const selectNone = (sel: Selection, filtered: readonly PickerNetwork[]): Selection =>
  setMany(sel, filtered, false);
export const invertSelection = (sel: Selection, filtered: readonly PickerNetwork[]): Selection =>
  withAll(sel, filtered, (on) => !on);

/** Number of selected networks among all, and the total. */
export function counter(
  networks: readonly PickerNetwork[],
  sel: Selection,
): { selected: number; total: number } {
  return { selected: networks.filter((n) => sel[n.id]).length, total: networks.length };
}

/** True when the selection differs from the stored tracked flags. */
export function isDirty(networks: readonly PickerNetwork[], sel: Selection): boolean {
  return networks.some((n) => !!sel[n.id] !== n.tracked);
}

/** Ids selected, for `track_only`. */
export function selectedNetworkIds(networks: readonly PickerNetwork[], sel: Selection): string[] {
  return networks.filter((n) => sel[n.id]).map((n) => n.id);
}
