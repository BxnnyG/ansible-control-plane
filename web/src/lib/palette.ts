/** Ranking and grouping for the command palette (pure, no DOM). */

export type PaletteGroup = 'Pages' | 'Templates' | 'Hosts' | 'Projects' | 'Inventories';

export const GROUP_ORDER: readonly PaletteGroup[] = [
  'Pages',
  'Templates',
  'Hosts',
  'Projects',
  'Inventories',
];

export interface PaletteItem {
  group: PaletteGroup;
  /** Stable key, unique across groups. */
  key: string;
  label: string;
  /** Secondary text, also searched (playbook, address, ...) with a lower weight. */
  detail?: string;
  href: string;
  /** Set for templates: the id the explicit Run action starts from. */
  templateId?: string;
}

/** Maximum rows shown per group. */
export const GROUP_CAP: Record<PaletteGroup, number> = {
  Pages: 8,
  Templates: 5,
  Hosts: 5,
  Projects: 4,
  Inventories: 4,
};

/**
 * Match quality of a text for a lowercase query; 0 means no match. Exact beats prefix, prefix
 * beats word prefix, word prefix beats substring.
 */
export function scoreText(text: string, q: string): number {
  const t = text.toLowerCase();
  if (t === q) return 100;
  if (t.startsWith(q)) return 80;
  if (t.split(/[\s._\-/:]+/).some((w) => w.startsWith(q))) return 60;
  if (t.includes(q)) return 40;
  return 0;
}

/** Score of an item for a query: label matches count fully, detail matches at half weight. */
export function scoreItem(item: PaletteItem, query: string): number {
  const q = query.trim().toLowerCase();
  if (!q) return 1;
  const a = scoreText(item.label, q);
  const b = item.detail ? Math.floor(scoreText(item.detail, q) / 2) : 0;
  return Math.max(a, b);
}

export interface PaletteRow {
  item: PaletteItem;
  /** True for the first row of a group; the UI renders a heading before it. */
  first: boolean;
}

/**
 * Filters, ranks and caps the items. Groups appear in GROUP_ORDER (pages first); inside a group
 * the best match comes first, ties keep the input order. An empty query shows pages only, so
 * the palette does not open as a wall of hosts.
 */
export function buildRows(items: readonly PaletteItem[], query: string): PaletteRow[] {
  const q = query.trim();
  const rows: PaletteRow[] = [];
  for (const group of GROUP_ORDER) {
    if (!q && group !== 'Pages') continue;
    const hits = items
      .map((item, idx) => ({ item, idx, score: item.group === group ? scoreItem(item, q) : 0 }))
      .filter((h) => h.score > 0)
      .sort((x, y) => y.score - x.score || x.idx - y.idx)
      .slice(0, GROUP_CAP[group]);
    hits.forEach((h, i) => rows.push({ item: h.item, first: i === 0 }));
  }
  return rows;
}
