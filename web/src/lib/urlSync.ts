import { replaceState } from '$app/navigation';
import { page } from '$app/state';
import { buildSearch } from './urlState';

/** Writes filter state into the current URL without adding history entries or reloading. */
export function syncUrl(updates: Record<string, string | boolean | null | undefined>): void {
  const search = buildSearch(page.url.searchParams, updates);
  if (search === page.url.search) return;
  try {
    // eslint-disable-next-line svelte/no-navigation-without-resolve -- same-page query update
    replaceState(page.url.pathname + search + page.url.hash, page.state);
  } catch {
    // The router is not ready yet; the next filter change syncs again.
  }
}
