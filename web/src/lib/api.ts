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

export type SecretType = 'ssh_key' | 'password' | 'token' | 'vault_password' | 'file';

/** Non-secret view of a stored secret; values are write-only and never returned. */
export interface SecretMeta {
  id: string;
  name: string;
  type: SecretType;
  description: string;
  /** e.g. { fingerprint, key_type } for ssh_key. */
  metadata: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export type ProjectSyncStatus = 'never' | 'ok' | 'error';

/** A Git project; credentials are referenced by secret id and never returned. */
export interface Project {
  id: string;
  name: string;
  slug: string;
  url: string;
  default_ref: string;
  credential_secret_id: string | null;
  https_username: string;
  ssh: boolean;
  host_key_pinned: boolean;
  host_key_fingerprints: string[];
  sync_interval_seconds: number | null;
  last_sync_status: ProjectSyncStatus;
  last_sync_error: string;
  last_sync_sha: string;
  last_sync_at: string | null;
  created_at: string;
  updated_at: string;
}

export type WebhookProvider = 'gitea' | 'github' | 'gitlab' | 'generic';

/** Webhook config of a project. `secret` is only present once, on enable or rotate. */
export interface ProjectWebhook {
  enabled: boolean;
  provider: WebhookProvider;
  has_secret: boolean;
  path: string;
  url?: string;
  secret?: string;
}

export interface ProjectInput {
  name: string;
  slug?: string;
  url: string;
  default_ref?: string;
  credential_secret_id?: string | null;
  https_username?: string;
  sync_interval_seconds?: number | null;
}

/** Fields omitted from an update stay untouched; null clears nullable fields. */
export type ProjectUpdate = Partial<ProjectInput>;

export interface HostKeyInfo {
  type: string;
  fingerprint: string;
}

export interface ProjectRevision {
  id: string;
  sha: string;
  synced_at: string;
  /** Scan result (ADR-0013); shape defined by the scanner. */
  scan: Record<string, unknown>;
  warnings: string[];
}

/** Machine-readable error codes of the projects API. */
export type ProjectErrorCode =
  | 'not_found'
  | 'conflict'
  | 'invalid'
  | 'bad_request'
  | 'not_ssh'
  | 'hostkey_not_pinned'
  | 'hostkey_mismatch'
  | 'sync_in_progress'
  | 'remote_failed';

export type InventoryMode = 'db' | 'git';

/** An inventory (ADR-0014). `version` is the optimistic lock, also sent as ETag. */
export interface Inventory {
  id: string;
  name: string;
  slug: string;
  mode: InventoryMode;
  project_id: string | null;
  path: string;
  write_branch: string;
  source_format: 'yaml' | 'ini';
  base_sha: string;
  version: number;
  last_import_at: string | null;
  hosts: number;
  groups: number;
  created_at: string;
  updated_at: string;
}

export interface InventoryInput {
  name: string;
  slug?: string;
  mode: InventoryMode;
  project_id?: string;
  path?: string;
  write_branch?: string;
}

/** Mode cannot be changed. */
export type InventoryUpdate = Partial<Omit<InventoryInput, 'mode'>>;

/** A group; `vars_yaml` keeps vault values in their encrypted `!vault` form. */
export interface InventoryGroup {
  id: string;
  name: string;
  parent_ids: string[];
  vars_yaml: string;
}

export interface InventoryGroupInput {
  name: string;
  parent_ids?: string[];
  vars_yaml?: string;
}

export type InventoryGroupUpdate = Partial<InventoryGroupInput>;

export interface InventoryHost {
  id: string;
  name: string;
  group_ids: string[];
  vars_yaml: string;
}

export interface InventoryHostInput {
  name: string;
  group_ids?: string[];
  vars_yaml?: string;
}

export type InventoryHostUpdate = Partial<InventoryHostInput>;

/** Source of an import; for git inventories everything defaults to the stored settings. */
export interface InventoryImportInput {
  project_id?: string;
  path?: string;
  format?: 'ini' | 'yaml';
}

export interface InventoryImportResult {
  inventory: Inventory;
  hosts: number;
  groups: number;
  sha: string;
  warnings: string[];
}

/** `ansible-inventory --list` shape; vault values are masked as "<vault>". */
export interface InventoryRender {
  _meta: { hostvars: Record<string, Record<string, unknown>> };
  [group: string]: unknown;
}

/** Machine-readable error codes of the inventories API. */
export type InventoryErrorCode =
  | 'not_found'
  | 'conflict'
  | 'invalid'
  | 'bad_request'
  | 'version_conflict'
  | 'git_conflict'
  | 'vault_requires_yaml'
  | 'project_not_synced';

export interface SecretCreate {
  name: string;
  type: SecretType;
  description?: string;
  /** Write-only. */
  value: string;
}

/** Omitted fields stay untouched; `value` replaces the stored secret. */
export interface SecretUpdate {
  name?: string;
  description?: string;
  value?: string;
}

export interface InventoryDiff {
  base_sha: string;
  branch: string;
  changed: boolean;
  paths: string[];
  diff: string;
}

export interface InventoryCommitResult {
  inventory: Inventory;
  branch: string;
  sha: string;
  paths: string[];
  branch_created: boolean;
  no_op: boolean;
}

export type VaultProvider = 'secret' | 'script';

/** A named vault password source; the password itself is never returned. */
export interface VaultIdentity {
  id: string;
  label: string;
  description: string;
  provider: VaultProvider;
  secret_id: string | null;
  script_path?: string;
  script_args?: string[];
  created_at: string;
  updated_at: string;
}

export interface VaultIdentityInput {
  label: string;
  description: string;
  provider: VaultProvider;
  secret_id: string | null;
  script_path: string;
  script_args: string[];
}

export interface VaultListing {
  sha: string;
  vault_files: { path: string; version: string; vault_id?: string }[];
  inline_vault: { file: string; paths: string[] }[];
}

export interface VaultDecrypted {
  plaintext: string;
  encoding: 'utf-8' | 'base64';
  identity: string;
}

export interface VaultWriteOptions {
  /** Empty selects the default branch (acp/vault). */
  branch: string;
  message: string;
}

export interface VaultEncryptInput extends VaultWriteOptions {
  path: string;
  /** YAML path of an inline value; empty encrypts the whole file. */
  key_path: string;
  /** Null encrypts the existing file content in place. */
  plaintext: string | null;
  /** Identity label; empty selects the default identity. */
  identity: string;
}

export interface VaultRekeyInput extends VaultWriteOptions {
  paths: string[];
  from: string;
  to: string;
}

export interface VaultWriteResult {
  branch: string;
  sha: string;
  paths: string[];
  branch_created: boolean;
  no_op: boolean;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly code: string = '',
    readonly detail: string = '',
    /** Conflicting paths of a git_conflict error. */
    readonly paths: string[] = [],
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

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  ifMatch?: number,
): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (ifMatch !== undefined) headers['If-Match'] = `"${ifMatch}"`;
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
    let code = '';
    let detail = '';
    let paths: string[] = [];
    try {
      const data = (await res.json()) as {
        error?: string;
        code?: string;
        detail?: string;
        paths?: string[];
      };
      paths = data.paths ?? [];
      if (data.error) message = data.error;
      code = data.code ?? '';
      detail = data.detail ?? '';
    } catch {
      // non-JSON error body: keep the status text
    }
    throw new ApiError(res.status, message, code, detail, paths);
  }
  if (res.status === 204) return undefined as T;
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
  secrets: {
    list: (ws: string) => request<SecretMeta[]>('GET', secretsPath(ws)),
    create: (ws: string, s: SecretCreate) => request<SecretMeta>('POST', secretsPath(ws), s),
    update: (ws: string, id: string, s: SecretUpdate) =>
      request<SecretMeta>('PUT', secretsPath(ws, id), s),
    remove: (ws: string, id: string) => request<void>('DELETE', secretsPath(ws, id)),
  },
  vaultIdentities: {
    list: (ws: string) => request<VaultIdentity[]>('GET', vaultIdentitiesPath(ws)),
    create: (ws: string, i: VaultIdentityInput) =>
      request<VaultIdentity>('POST', vaultIdentitiesPath(ws), i),
    update: (ws: string, id: string, i: VaultIdentityInput) =>
      request<VaultIdentity>('PUT', vaultIdentitiesPath(ws, id), i),
    remove: (ws: string, id: string) => request<void>('DELETE', vaultIdentitiesPath(ws, id)),
  },
  vault: {
    list: (ws: string, project: string) =>
      request<VaultListing>('GET', projectsPath(ws, project, 'vault')),
    decrypt: (ws: string, project: string, path: string, keyPath = '') =>
      request<VaultDecrypted>('POST', projectsPath(ws, project, 'vault/decrypt'), {
        path,
        key_path: keyPath,
      }),
    encrypt: (ws: string, project: string, i: VaultEncryptInput) =>
      request<VaultWriteResult>('POST', projectsPath(ws, project, 'vault/encrypt'), i),
    rekey: (ws: string, project: string, i: VaultRekeyInput) =>
      request<VaultWriteResult>('POST', projectsPath(ws, project, 'vault/rekey'), i),
  },
  projects: {
    list: (ws: string) => request<Project[]>('GET', projectsPath(ws)),
    get: (ws: string, id: string) => request<Project>('GET', projectsPath(ws, id)),
    create: (ws: string, p: ProjectInput) => request<Project>('POST', projectsPath(ws), p),
    update: (ws: string, id: string, p: ProjectUpdate) =>
      request<Project>('PUT', projectsPath(ws, id), p),
    remove: (ws: string, id: string) => request<void>('DELETE', projectsPath(ws, id)),
    scanHostKeys: (ws: string, id: string) =>
      request<{ keys: HostKeyInfo[] }>('POST', projectsPath(ws, id, 'hostkey/scan')),
    confirmHostKey: (ws: string, id: string, fingerprint: string) =>
      request<Project>('POST', projectsPath(ws, id, 'hostkey/confirm'), { fingerprint }),
    test: (ws: string, id: string) =>
      request<{ ok: boolean }>('POST', projectsPath(ws, id, 'test')),
    webhook: (ws: string, id: string) =>
      request<ProjectWebhook>('GET', projectsPath(ws, id, 'webhook')),
    setWebhook: (ws: string, id: string, enabled: boolean, provider: WebhookProvider) =>
      request<ProjectWebhook>('PUT', projectsPath(ws, id, 'webhook'), { enabled, provider }),
    rotateWebhook: (ws: string, id: string) =>
      request<ProjectWebhook>('POST', projectsPath(ws, id, 'webhook/rotate')),
    removeWebhook: (ws: string, id: string) =>
      request<void>('DELETE', projectsPath(ws, id, 'webhook')),
    sync: (ws: string, id: string) =>
      request<ProjectRevision>('POST', projectsPath(ws, id, 'sync')),
    revisions: (ws: string, id: string) =>
      request<ProjectRevision[]>('GET', projectsPath(ws, id, 'revisions')),
    latestRevision: (ws: string, id: string) =>
      request<ProjectRevision>('GET', projectsPath(ws, id, 'revisions/latest')),
  },
  inventories: {
    list: (ws: string) => request<Inventory[]>('GET', inventoriesPath(ws)),
    get: (ws: string, id: string) => request<Inventory>('GET', inventoriesPath(ws, id)),
    create: (ws: string, i: InventoryInput) => request<Inventory>('POST', inventoriesPath(ws), i),
    update: (ws: string, id: string, i: InventoryUpdate, version?: number) =>
      request<Inventory>('PUT', inventoriesPath(ws, id), i, version),
    remove: (ws: string, id: string) => request<void>('DELETE', inventoriesPath(ws, id)),
    import: (ws: string, id: string, i: InventoryImportInput = {}, version?: number) =>
      request<InventoryImportResult>('POST', inventoriesPath(ws, id, 'import'), i, version),
    render: (ws: string, id: string, exportMode = false) =>
      request<InventoryRender>(
        'GET',
        inventoriesPath(ws, id, 'render') + (exportMode ? '?export=true' : ''),
      ),
    diff: (ws: string, id: string) =>
      request<InventoryDiff>('GET', inventoriesPath(ws, id, 'diff')),
    commit: (ws: string, id: string, message: string, version?: number) =>
      request<InventoryCommitResult>(
        'POST',
        inventoriesPath(ws, id, 'commit'),
        { message },
        version,
      ),
    groups: (ws: string, id: string) =>
      request<InventoryGroup[]>('GET', inventoriesPath(ws, id, 'groups')),
    createGroup: (ws: string, id: string, g: InventoryGroupInput, version?: number) =>
      request<InventoryGroup>('POST', inventoriesPath(ws, id, 'groups'), g, version),
    updateGroup: (ws: string, id: string, gid: string, g: InventoryGroupUpdate, version?: number) =>
      request<InventoryGroup>('PUT', inventoriesPath(ws, id, `groups/${gid}`), g, version),
    removeGroup: (ws: string, id: string, gid: string, version?: number) =>
      request<void>('DELETE', inventoriesPath(ws, id, `groups/${gid}`), undefined, version),
    hosts: (ws: string, id: string) =>
      request<InventoryHost[]>('GET', inventoriesPath(ws, id, 'hosts')),
    createHost: (ws: string, id: string, h: InventoryHostInput, version?: number) =>
      request<InventoryHost>('POST', inventoriesPath(ws, id, 'hosts'), h, version),
    updateHost: (ws: string, id: string, hid: string, h: InventoryHostUpdate, version?: number) =>
      request<InventoryHost>('PUT', inventoriesPath(ws, id, `hosts/${hid}`), h, version),
    removeHost: (ws: string, id: string, hid: string, version?: number) =>
      request<void>('DELETE', inventoriesPath(ws, id, `hosts/${hid}`), undefined, version),
    addMember: (ws: string, id: string, gid: string, hid: string, version?: number) =>
      request<void>(
        'PUT',
        inventoriesPath(ws, id, `groups/${gid}/hosts/${hid}`),
        undefined,
        version,
      ),
    removeMember: (ws: string, id: string, gid: string, hid: string, version?: number) =>
      request<void>(
        'DELETE',
        inventoriesPath(ws, id, `groups/${gid}/hosts/${hid}`),
        undefined,
        version,
      ),
  },
};

function secretsPath(ws: string, id?: string): string {
  const p = `/api/v1/workspaces/${encodeURIComponent(ws)}/secrets`;
  return id ? `${p}/${encodeURIComponent(id)}` : p;
}

function vaultIdentitiesPath(ws: string, id?: string): string {
  const p = `/api/v1/workspaces/${encodeURIComponent(ws)}/vault-identities`;
  return id ? `${p}/${encodeURIComponent(id)}` : p;
}

function inventoriesPath(ws: string, id?: string, sub?: string): string {
  let p = `/api/v1/workspaces/${encodeURIComponent(ws)}/inventories`;
  if (id) p += `/${encodeURIComponent(id)}`;
  if (sub) p += `/${sub}`;
  return p;
}

function projectsPath(ws: string, id?: string, sub?: string): string {
  let p = `/api/v1/workspaces/${encodeURIComponent(ws)}/projects`;
  if (id) p += `/${encodeURIComponent(id)}`;
  if (sub) p += `/${sub}`;
  return p;
}

/** Login URL for the OIDC redirect flow; the caller must pass a validated path. */
export function oidcLoginUrl(returnTo: string): string {
  return `/api/auth/oidc/login?return_to=${encodeURIComponent(returnTo)}`;
}
