/** Slug of the workspace the first-run wizard applies to. */
export const SETUP_WORKSPACE = 'default';

/** Picks the active workspace: stored choice, then the default workspace, then the first one. */
export function pickWorkspace(slugs: readonly string[], stored: string | null): string | null {
  if (stored && slugs.includes(stored)) return stored;
  if (slugs.includes(SETUP_WORKSPACE)) return SETUP_WORKSPACE;
  return slugs[0] ?? null;
}
