import { describe, expect, it } from 'vitest';
import type { InventoryGroup } from './api';
import { buildGroupTree, flattenTree } from './groupTree';

const g = (id: string, parent_ids: string[] = []): InventoryGroup => ({
  id,
  name: id,
  parent_ids,
  vars_yaml: '',
});

describe('buildGroupTree', () => {
  it('nests children under parents', () => {
    const t = buildGroupTree([g('web', ['all']), g('all'), g('db', ['all'])]);
    expect(t.map((n) => n.group.id)).toEqual(['all']);
    expect(t[0].children.map((n) => n.group.id)).toEqual(['db', 'web']);
  });

  it('shows multi-parent groups under each parent and flags repeats', () => {
    const t = buildGroupTree([g('a'), g('b'), g('c', ['a', 'b'])]);
    const flat = flattenTree(t);
    expect(flat.map((x) => x.node.group.id)).toEqual(['a', 'c', 'b', 'c']);
    expect(flat[1].node.repeated).toBe(false);
    expect(flat[3].node.repeated).toBe(true);
    expect(flat[1].node.parentNames).toEqual(['a', 'b']);
  });

  it('survives cycles and unknown parents', () => {
    const t = buildGroupTree([g('x', ['y']), g('y', ['x']), g('z', ['missing'])]);
    const ids = flattenTree(t).map((e) => e.node.group.id);
    expect(ids).toContain('x');
    expect(ids).toContain('y');
    expect(ids).toContain('z');
  });
});
