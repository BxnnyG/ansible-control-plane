import { describe, expect, it } from 'vitest';
import {
  counter,
  filterNetworks,
  groupByConnector,
  groupState,
  initialSelection,
  invertSelection,
  isDirty,
  selectAll,
  selectNone,
  selectedNetworkIds,
  toggleGroup,
  hideEmpty,
  networkChipLabel,
  sortNetworks,
  type PickerNetwork,
} from './networkPicker';

const nets: PickerNetwork[] = [
  {
    id: 'a',
    cidr: '192.0.2.0/24',
    description: 'Servers',
    vlan: 10,
    connector: 'opn-b',
    tracked: true,
  },
  {
    id: 'b',
    cidr: '198.51.100.0/24',
    description: 'Guest wifi',
    vlan: 20,
    connector: 'opn-a',
    tracked: false,
  },
  { id: 'c', cidr: '203.0.113.0/24', interface: 'igb0', connector: 'opn-a', tracked: true },
  { id: 'd', cidr: '10.0.0.0/8', connector: 'pve', tracked: false },
];

describe('filterNetworks', () => {
  it('returns everything for an empty query', () => {
    expect(filterNetworks(nets, '  ')).toHaveLength(4);
  });
  it('matches cidr, description, vlan, interface and connector', () => {
    expect(filterNetworks(nets, '198.51').map((n) => n.id)).toEqual(['b']);
    expect(filterNetworks(nets, 'GUEST').map((n) => n.id)).toEqual(['b']);
    expect(filterNetworks(nets, 'vlan 20').map((n) => n.id)).toEqual(['b']);
    expect(filterNetworks(nets, 'igb0').map((n) => n.id)).toEqual(['c']);
    expect(filterNetworks(nets, 'opn-a').map((n) => n.id)).toEqual(['b', 'c']);
  });
  it('requires every term to match', () => {
    expect(filterNetworks(nets, 'opn-a guest').map((n) => n.id)).toEqual(['b']);
    expect(filterNetworks(nets, 'nomatch')).toEqual([]);
  });
});

describe('groupByConnector', () => {
  it('sorts groups by connector and keeps item order', () => {
    const g = groupByConnector(nets);
    expect(g.map((x) => x.connector)).toEqual(['opn-a', 'opn-b', 'pve']);
    expect(g[0]!.items.map((n) => n.id)).toEqual(['b', 'c']);
  });
});

describe('selection', () => {
  const sel = initialSelection(nets);
  it('starts from the tracked flags', () => {
    expect(sel).toEqual({ a: true, b: false, c: true, d: false });
    expect(isDirty(nets, sel)).toBe(false);
  });
  it('reports tri-state per group', () => {
    const [opnA] = groupByConnector(nets);
    expect(groupState(opnA!.items, sel)).toBe('some');
    expect(groupState([nets[0]!], sel)).toBe('all');
    expect(groupState([nets[3]!], sel)).toBe('none');
  });
  it('toggleGroup selects a partial group, then clears a full one', () => {
    const [opnA] = groupByConnector(nets);
    const all = toggleGroup(sel, opnA!.items);
    expect(groupState(opnA!.items, all)).toBe('all');
    expect(groupState(opnA!.items, toggleGroup(all, opnA!.items))).toBe('none');
  });
  it('all/none/invert only touch the filtered set', () => {
    const filtered = filterNetworks(nets, 'opn-a');
    expect(selectAll(sel, filtered)).toEqual({ a: true, b: true, c: true, d: false });
    expect(selectNone(sel, filtered)).toEqual({ a: true, b: false, c: false, d: false });
    expect(invertSelection(sel, filtered)).toEqual({ a: true, b: true, c: false, d: false });
  });
  it('counts across all networks, not just the filtered ones', () => {
    expect(counter(nets, sel)).toEqual({ selected: 2, total: 4 });
  });
  it('detects changes and restores to clean when reverted', () => {
    const changed = { ...sel, d: true };
    expect(isDirty(nets, changed)).toBe(true);
    expect(isDirty(nets, { ...changed, d: false })).toBe(false);
  });
  it('lists selected ids in network order', () => {
    expect(selectedNetworkIds(nets, { ...sel, d: true })).toEqual(['a', 'c', 'd']);
  });
});

describe('sortNetworks / hideEmpty / networkChipLabel', () => {
  const mk = (id: string, cidr: string, host_count: number, tracked = false) => ({
    id,
    cidr,
    connector: 'c',
    tracked,
    host_count,
  });
  const nets = [
    mk('a', '192.0.2.0/24', 1),
    mk('b', '198.51.100.0/24', 9),
    mk('c', '203.0.113.0/24', 0),
  ];

  it('puts selected first, then by host count', () => {
    const out = sortNetworks(nets, { a: true }, 'hosts').map((n) => n.id);
    expect(out).toEqual(['a', 'b', 'c']);
    expect(sortNetworks(nets, {}, 'hosts').map((n) => n.id)).toEqual(['b', 'a', 'c']);
    expect(sortNetworks(nets, {}, 'default').map((n) => n.id)).toEqual(['a', 'b', 'c']);
  });

  it('hides empty networks except selected ones', () => {
    expect(hideEmpty(nets, {}).map((n) => n.id)).toEqual(['a', 'b']);
    expect(hideEmpty(nets, { c: true }).map((n) => n.id)).toEqual(['a', 'b', 'c']);
  });

  it('builds chip labels', () => {
    expect(networkChipLabel({ ...nets[0], description: 'Lab' })).toBe('Lab 192.0.2.0/24');
    expect(networkChipLabel(nets[0])).toBe('192.0.2.0/24');
  });
});
