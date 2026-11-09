import type { InventoryGroup, InventoryHost } from './api';
import type { GroupNode } from './groupTree';

export interface TreeRow {
  /** Unique per occurrence (a multi-parent group appears under each parent). */
  key: string;
  node: GroupNode;
  depth: number;
  parentKey: string | null;
  hasChildren: boolean;
  expanded: boolean;
}

/**
 * Flattens the visible part of the tree. With a non-empty filter, nodes that match by name
 * and all their ancestors stay visible and everything on the way is expanded.
 */
export function visibleRows(
  nodes: readonly GroupNode[],
  collapsed: ReadonlySet<string>,
  filter: string,
): TreeRow[] {
  const q = filter.trim().toLowerCase();
  const matches = (n: GroupNode): boolean =>
    n.group.name.toLowerCase().includes(q) || n.children.some(matches);
  const out: TreeRow[] = [];
  const walk = (list: readonly GroupNode[], depth: number, parentKey: string | null) => {
    for (const node of list) {
      if (q && !matches(node)) continue;
      const key = `${parentKey ?? ''}/${node.group.id}`;
      const hasChildren = node.children.length > 0;
      const expanded = hasChildren && (q !== '' || !collapsed.has(key));
      out.push({ key, node, depth, parentKey, hasChildren, expanded });
      if (expanded) walk(node.children, depth + 1, key);
    }
  };
  walk(nodes, 0, null);
  return out;
}

/** Host counts per group id: direct members and members including all descendant groups. */
export function hostCounts(
  groups: readonly InventoryGroup[],
  hosts: readonly InventoryHost[],
): Map<string, { direct: number; total: number }> {
  const kids = new Map<string, string[]>();
  for (const g of groups) for (const p of g.parent_ids) kids.set(p, [...(kids.get(p) ?? []), g.id]);
  const members = new Map<string, Set<string>>();
  for (const h of hosts) {
    for (const gid of h.group_ids) {
      if (!members.has(gid)) members.set(gid, new Set());
      members.get(gid)?.add(h.id);
    }
  }
  const out = new Map<string, { direct: number; total: number }>();
  for (const g of groups) {
    const seen = new Set<string>();
    const all = new Set<string>();
    const stack = [g.id];
    while (stack.length > 0) {
      const id = stack.pop() as string;
      if (seen.has(id)) continue;
      seen.add(id);
      for (const m of members.get(id) ?? []) all.add(m);
      stack.push(...(kids.get(id) ?? []));
    }
    out.set(g.id, { direct: members.get(g.id)?.size ?? 0, total: all.size });
  }
  return out;
}
