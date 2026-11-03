import { api, ApiError, type Me, type WorkspaceDetail } from './api';
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
    try {
      this.me = await api.me();
    } catch (e) {
      this.me = null;
      this.status = e instanceof ApiError && e.status === 401 ? 'anonymous' : 'error';
      return;
    }
    this.slug = pickWorkspace(
      this.me.memberships.map((m) => m.slug),
      storedSlug(),
    );
    await this.loadWorkspace();
    this.status = 'ready';
  }

  async loadWorkspace(): Promise<void> {
    this.workspace = null;
    this.needsSetup = false;
    if (!this.slug) return;
    try {
      this.workspace = await api.workspace(this.slug);
      if (this.slug === SETUP_WORKSPACE && this.can('workspace.manage')) {
        this.needsSetup = !(await api.setupStatus(this.slug)).completed;
      }
    } catch {
      // The shell still works without workspace details; actions stay hidden.
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
    this.me = null;
    this.workspace = null;
    this.slug = null;
    this.needsSetup = false;
    this.status = 'anonymous';
  }
}

export const session = new SessionState();
