export interface NavItem {
  label: string;
  href: string;
  /** SVG path data (24x24 viewbox, stroke icon). */
  icon: string;
  /** Permission required to see the entry; absent means always visible. */
  permission?: string;
  /** Sidebar group heading; empty for ungrouped entries. */
  section: NavSection;
}

export type NavSection = 'Operate' | 'Configure' | 'Security' | '';

export const NAV_SECTIONS: readonly NavSection[] = ['Operate', 'Configure', 'Security', ''];

export const NAV_ITEMS: readonly NavItem[] = [
  {
    label: 'Dashboard',
    href: '/',
    icon: 'M3 13h8V3H3zM13 21h8v-8h-8zM13 3v6h8V3zM3 21h8v-6H3z',
    section: 'Operate',
  },
  { label: 'Runs', href: '/runs', icon: 'M8 5v14l11-7z', section: 'Operate' },
  {
    label: 'Templates',
    href: '/templates',
    icon: 'M5 4h14v6H5zM5 14h6v6H5zM15 14h4v6h-4z',
    section: 'Operate',
  },
  {
    label: 'Fleet',
    href: '/fleet',
    icon: 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01',
    permission: 'fleet.read',
    section: 'Operate',
  },
  {
    label: 'Projects',
    href: '/projects',
    icon: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z',
    section: 'Configure',
  },
  {
    label: 'Inventories',
    href: '/inventories',
    icon: 'M4 7h16M4 12h16M4 17h10',
    section: 'Configure',
  },
  {
    label: 'Connectors',
    href: '/connectors',
    icon: 'M9 7V3M15 7V3M7 7h10v5a5 5 0 0 1-10 0zM12 17v4',
    permission: 'connector.read',
    section: 'Configure',
  },
  {
    label: 'Notifications',
    href: '/notifications',
    icon: 'M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9M10 21a2 2 0 0 0 4 0',
    permission: 'notification.read',
    section: 'Configure',
  },
  {
    label: 'Secrets',
    href: '/secrets',
    icon: 'M7 11V8a5 5 0 0 1 10 0v3M5 11h14v9H5z',
    section: 'Security',
  },
  {
    label: 'Vault',
    href: '/vault',
    icon: 'M5 4h14v16H5zM12 9a2 2 0 1 0 0 4 2 2 0 0 0 0-4zM12 13v3',
    section: 'Security',
  },
  {
    label: 'Settings',
    href: '/settings',
    icon: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8zM12 2v3M12 19v3M2 12h3M19 12h3',
    section: '',
  },
];

/** Whether the entry is the active one for a path (the dashboard only matches exactly). */
export function isCurrent(item: NavItem, pathname: string): boolean {
  if (item.href === '/') return pathname === '/';
  return pathname === item.href || pathname.startsWith(item.href + '/');
}

/** Entries grouped by section in display order; empty sections are dropped. */
export function groupNav(items: readonly NavItem[]): { section: NavSection; items: NavItem[] }[] {
  return NAV_SECTIONS.map((section) => ({
    section,
    items: items.filter((i) => i.section === section),
  })).filter((g) => g.items.length > 0);
}

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
