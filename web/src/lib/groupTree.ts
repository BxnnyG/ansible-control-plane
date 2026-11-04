import type { InventoryGroup } from './api';

export interface GroupNode {
  group: InventoryGroup;
  children: GroupNode[];
  /** True when this node is a repeated occurrence of a multi-parent group. */
  repeated: boolean;
  /** Names of all parents (shown when there is more than one). */
  parentNames: string[];
}

/**
 * Builds a forest from groups with parent_ids. Groups with several parents appear
 * under each parent (marked repeated after the first); cycles and unknown parents
 * are tolerated: unknown parents are ignored, cycle members become roots.
 */
export function buildGroupTree(groups: readonly InventoryGroup[]): GroupNode[] {
  const byId = new Map(groups.map((g) => [g.id, g]));
  const kids = new Map<string, InventoryGroup[]>();
  const roots: InventoryGroup[] = [];
  for (const g of groups) {
    const parents = g.parent_ids.filter((p) => byId.has(p) && p !== g.id);
    if (parents.length === 0) roots.push(g);
    for (const p of parents) kids.set(p, [...(kids.get(p) ?? []), g]);
  }
  const byName = (a: InventoryGroup, b: InventoryGroup) => a.name.localeCompare(b.name);
  const seen = new Set<string>();

  const make = (g: InventoryGroup, path: readonly string[]): GroupNode => {
    const repeated = seen.has(g.id);
    seen.add(g.id);
    const children =
      repeated || path.includes(g.id)
        ? []
        : [...(kids.get(g.id) ?? [])].sort(byName).map((c) => make(c, [...path, g.id]));
    return {
      group: g,
      children,
      repeated,
      parentNames: g.parent_ids.map((p) => byId.get(p)?.name).filter((n): n is string => !!n),
    };
  };

  const tree = roots.sort(byName).map((g) => make(g, []));
  // Groups only reachable through a cycle were never visited: surface them as roots.
  for (const g of [...groups].sort(byName)) if (!seen.has(g.id)) tree.push(make(g, []));
  return tree;
}

/** Flattens the tree depth-first with indentation levels (for simple list rendering). */
export function flattenTree(
  nodes: readonly GroupNode[],
  depth = 0,
): { node: GroupNode; depth: number }[] {
  return nodes.flatMap((n) => [{ node: n, depth }, ...flattenTree(n.children, depth + 1)]);
}
