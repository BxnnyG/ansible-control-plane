export interface NavItem {
  label: string;
  href: string;
  /** SVG path data (24x24 viewbox, stroke icon). */
  icon: string;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { label: 'Fleet', href: '/fleet', icon: 'M4 6h16M4 12h16M4 18h16' },
  { label: 'Runs', href: '/runs', icon: 'M8 5v14l11-7z' },
  { label: 'Templates', href: '/templates', icon: 'M5 4h14v6H5zM5 14h6v6H5zM15 14h4v6h-4z' },
  { label: 'Inventories', href: '/inventories', icon: 'M4 7h16M4 12h16M4 17h10' },
  {
    label: 'Projects',
    href: '/projects',
    icon: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z',
  },
  {
    label: 'Settings',
    href: '/settings',
    icon: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8zM12 2v3M12 19v3M2 12h3M19 12h3',
  },
];

/** Case-insensitive substring filter used by the command palette. */
export function filterNav(items: readonly NavItem[], query: string): NavItem[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...items];
  return items.filter((i) => i.label.toLowerCase().includes(q));
}
