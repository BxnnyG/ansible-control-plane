import { api, ApiError, clearApiCache, type Me, type WorkspaceDetail } from './api';
import { toasts } from './toasts.svelte';
import { can } from './permissions';
import { SETUP_WORKSPACE, pickWorkspace } from './workspace';

const KEY = 'acp-workspace';

type Status = 'loading' | 'anonymous' | 'ready' | 'error';

function storedSlug(): string | null {
  try {
    return localStorage.getItem(KEY);
  } catch {
    return null;
  }
}

class SessionState {
  status = $state<Status>('loading');
  me = $state<Me | null>(null);
  slug = $state<string | null>(null);
  workspace = $state<WorkspaceDetail | null>(null);
  needsSetup = $state(false);

  /** UI hint only; the server decides. */
  can(permission: string): boolean {
    return this.workspace ? can(this.workspace.permissions, permission) : false;
  }

  async load(): Promise<void> {
    clearApiCache(); // a new sign-in must never see another user's cached lists
    // The stored workspace slug lets us fetch the workspace while `me` is in flight.
    const guess = storedSlug();
    const early = guess ? api.workspace(guess) : null;
    early?.catch(() => undefined); // handled below; avoids an unhandled rejection
    try {
      this.me = await api.me();
    } catch (e) {
      this.me = null;
      this.status = e instanceof ApiError && e.status === 401 ? 'anonymous' : 'error';
      return;
    }
    this.slug = pickWorkspace(
      this.me.memberships.map((m) => m.slug),
      guess,
    );
    await this.loadWorkspace(this.slug === guess ? early : null);
    this.status = 'ready';
  }

  /** Loads the active workspace; the setup status never blocks and may arrive after `ready`. */
  async loadWorkspace(prefetched: Promise<WorkspaceDetail> | null = null): Promise<void> {
    this.workspace = null;
    this.needsSetup = false;
    const slug = this.slug;
    if (!slug) return;
    try {
      this.workspace = await (prefetched ?? api.workspace(slug));
    } catch {
      // The shell still works without workspace details; actions stay hidden.
      return;
    }
    if (slug === SETUP_WORKSPACE && this.can('workspace.manage')) {
      void api
        .setupStatus(slug)
        .then((st) => {
          if (this.slug === slug) this.needsSetup = !st.completed;
        })
        .catch(() => undefined);
    }
  }

  /** Reloads the user and memberships (e.g. after a workspace rename). */
  async refreshMe(): Promise<void> {
    try {
      this.me = await api.me();
    } catch {
      // keep the previous data
    }
  }

  async select(slug: string): Promise<void> {
    clearApiCache();
    toasts.clear();
    this.slug = slug;
    try {
      localStorage.setItem(KEY, slug);
    } catch {
      // storage unavailable: selection lasts for this page load only
    }
    await this.loadWorkspace();
  }

  markSetupDone(): void {
    this.needsSetup = false;
  }

  reset(): void {
    clearApiCache();
    toasts.clear();
    this.me = null;
    this.workspace = null;
    this.slug = null;
    this.needsSetup = false;
    this.status = 'anonymous';
  }
}

export const session = new SessionState();
