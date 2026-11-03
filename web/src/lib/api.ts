/**
 * Typed client for the backend API. Types are written by hand until OpenAPI
 * generation is set up (open point); keep them in sync with the Go handlers.
 */

export interface AuthConfig {
  oidc: boolean;
  breakglass: boolean;
}

export interface Membership {
  workspace_id: string;
  slug: string;
  name: string;
  role: string;
  scoped: boolean;
}

export interface Me {
  id: string;
  email: string;
  display_name: string;
  memberships: Membership[];
}

export interface WorkspaceDetail {
  slug: string;
  name: string;
  description: string;
  role: string;
  scoped: boolean;
  permissions: string[];
}

export interface RoleMapping {
  idp_role: string;
  role: string;
}

export interface SetupStatus {
  name: string;
  completed: boolean;
  completed_at: string | null;
  auth: {
    oidc: boolean;
    issuer_host: string;
    role_mappings: RoleMapping[];
    breakglass: boolean;
  };
}

export interface BreakGlassCredentials {
  username: string;
  password: string;
  totp: string;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

const CSRF_COOKIES = ['__Host-acp_csrf', 'acp_csrf'];

/** Reads the script-readable CSRF cookie (the name depends on HTTPS). */
export function csrfToken(cookies: string = document.cookie): string {
  for (const part of cookies.split(';')) {
    const [name, ...rest] = part.trim().split('=');
    if (CSRF_COOKIES.includes(name)) return decodeURIComponent(rest.join('='));
  }
  return '';
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET') headers['X-CSRF-Token'] = csrfToken();
  const res = await fetch(path, {
    method,
    headers,
    credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const data = (await res.json()) as { error?: string };
      if (data.error) message = data.error;
    } catch {
      // non-JSON error body: keep the status text
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}

export const api = {
  authConfig: () => request<AuthConfig>('GET', '/api/auth/config'),
  me: () => request<Me>('GET', '/api/v1/me'),
  breakGlassLogin: (c: BreakGlassCredentials) =>
    request<{ status: string }>('POST', '/api/auth/breakglass/login', c),
  logout: () => request<{ redirect: string }>('POST', '/api/auth/logout'),
  workspace: (slug: string) =>
    request<WorkspaceDetail>('GET', `/api/v1/workspaces/${encodeURIComponent(slug)}`),
  setupStatus: (slug: string) =>
    request<SetupStatus>('GET', `/api/v1/workspaces/${encodeURIComponent(slug)}/setup`),
  setWorkspaceName: (slug: string, name: string) =>
    request<SetupStatus>('PUT', `/api/v1/workspaces/${encodeURIComponent(slug)}/setup`, { name }),
  completeSetup: (slug: string) =>
    request<SetupStatus>('POST', `/api/v1/workspaces/${encodeURIComponent(slug)}/setup/complete`),
};

/** Login URL for the OIDC redirect flow; the caller must pass a validated path. */
export function oidcLoginUrl(returnTo: string): string {
  return `/api/auth/oidc/login?return_to=${encodeURIComponent(returnTo)}`;
}
