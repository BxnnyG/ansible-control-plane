/**
 * Maps old Semaphore UI paths (bookmarks, e-mail links) to their ACP
 * equivalent. Returns a relative path or null when the path is not legacy.
 * Only the pathname is inspected; hosts are never involved.
 */
export function legacyRedirect(pathname: string): string | null {
  const path = pathname.replace(/\/+$/, '') || '/';
  if (path === '/auth' || path.startsWith('/auth/')) return '/login';
  const m = /^\/project\/[^/]+(?:\/(.*))?$/.exec(path);
  if (!m) return null;
  const rest = m[1] ?? '';
  const seg = rest.split('/');
  // /project/<n>/views/<v>/templates... behaves like /project/<n>/templates...
  const head = seg[0] === 'views' ? (seg[2] ?? '') : seg[0];
  switch (head) {
    case 'templates':
      return '/templates';
    case 'history':
    case 'tasks':
      return '/runs';
    case 'inventory':
      return '/inventories';
    case 'keys':
      return '/secrets';
    default:
      return '/';
  }
}
