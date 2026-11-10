import { describe, expect, it } from 'vitest';
import type { InventoryGroup, InventoryHost } from './api';
import { hostCounts, visibleRows } from './groupBrowse';
import { buildGroupTree } from './groupTree';

const g = (id: string, parent_ids: string[] = []) =>
  ({ id, name: id, parent_ids }) as InventoryGroup;
const h = (id: string, group_ids: string[]) => ({ id, name: id, group_ids }) as InventoryHost;

describe('groupBrowse', () => {
  const groups = [g('all'), g('web', ['all']), g('db', ['all']), g('nginx', ['web'])];
  const tree = buildGroupTree(groups);

  it('hides collapsed subtrees and expands matches while filtering', () => {
    expect(visibleRows(tree, new Set(), '').map((r) => r.node.group.id)).toEqual([
      'all',
      'db',
      'web',
      'nginx',
    ]);
    expect(visibleRows(tree, new Set(['/all/web']), '').map((r) => r.node.group.id)).toEqual([
      'all',
      'db',
      'web',
    ]);
    expect(visibleRows(tree, new Set(['/all']), 'ngi').map((r) => r.node.group.id)).toEqual([
      'all',
      'web',
      'nginx',
    ]);
  });

  it('counts direct and descendant hosts without double counting', () => {
    const c = hostCounts(groups, [h('1', ['web', 'nginx']), h('2', ['db']), h('3', [])]);
    expect(c.get('web')).toEqual({ direct: 1, total: 1 });
    expect(c.get('all')).toEqual({ direct: 0, total: 2 });
    expect(c.get('nginx')).toEqual({ direct: 1, total: 1 });
  });
});
