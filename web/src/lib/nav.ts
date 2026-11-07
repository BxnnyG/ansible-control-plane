export interface NavItem {
  label: string;
  href: string;
  /** SVG path data (24x24 viewbox, stroke icon). */
  icon: string;
  /** Permission required to see the entry; absent means always visible. */
  permission?: string;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { label: 'Fleet', href: '/fleet', icon: 'M4 6h16M4 12h16M4 18h16', permission: 'fleet.read' },
  {
    label: 'Connectors',
    href: '/connectors',
    icon: 'M9 7V3M15 7V3M7 7h10v5a5 5 0 0 1-10 0zM12 17v4',
    permission: 'connector.read',
  },
  {
    label: 'Notifications',
    href: '/notifications',
    icon: 'M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9M10 21a2 2 0 0 0 4 0',
    permission: 'notification.read',
  },
  { label: 'Runs', href: '/runs', icon: 'M8 5v14l11-7z' },
  { label: 'Templates', href: '/templates', icon: 'M5 4h14v6H5zM5 14h6v6H5zM15 14h4v6h-4z' },
  { label: 'Inventories', href: '/inventories', icon: 'M4 7h16M4 12h16M4 17h10' },
  {
    label: 'Projects',
    href: '/projects',
    icon: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z',
  },
  {
    label: 'Secrets',
    href: '/secrets',
    icon: 'M7 11V8a5 5 0 0 1 10 0v3M5 11h14v9H5z',
  },
  {
    label: 'Vault',
    href: '/vault',
    icon: 'M5 4h14v16H5zM12 9a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM12 13v3',
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

/** Entries the caller may see according to the permission hints. */
export function visibleNav(
  items: readonly NavItem[],
  allowed: (permission: string) => boolean,
): NavItem[] {
  return items.filter((i) => !i.permission || allowed(i.permission));
}
