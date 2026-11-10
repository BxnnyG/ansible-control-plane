import { redirect } from '@sveltejs/kit';
import { legacyRedirect } from '$lib/legacyRoutes';

// Pure SPA: no server rendering, no prerendering; the Go binary serves index.html as fallback.
export const ssr = false;
export const prerender = false;

// Old Semaphore bookmarks (/auth/login, /project/<n>/...) resolve to relative ACP paths.
export function load({ url }: { url: URL }) {
  const target = legacyRedirect(url.pathname);
  if (target) redirect(307, target);
}
