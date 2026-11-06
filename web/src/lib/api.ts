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

export type SecretType =
  'ssh_key' | 'password' | 'token' | 'vault_password' | 'file' | 'api_key_pair' | 'api_token';

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

export type RunStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'lost';
export type RunTrigger = 'manual' | 'schedule' | 'webhook' | 'api';

/** Per-run overrides; the template's allow_*_override flags decide what is accepted. */
export interface RunOverrides {
  limit?: string;
  tags?: string;
  skip_tags?: string;
  extra_vars?: Record<string, unknown>;
  check?: boolean;
  diff?: boolean;
  verbosity?: number;
}

export interface Template {
  id: string;
  project_id: string;
  inventory_id: string;
  name: string;
  slug: string;
  description: string;
  playbook: string;
  extra_vars: Record<string, unknown>;
  env_vars: Record<string, unknown>;
  ssh_secret_id: string | null;
  become_secret_id: string | null;
  vault_identity_ids: string[];
  default_limit: string;
  default_tags: string;
  default_skip_tags: string;
  verbosity: number;
  check_mode: boolean;
  diff_mode: boolean;
  /** Survey fields (list or `{fields}`); null when the template has none. */
  survey: unknown;
  max_concurrent: number;
  allow_limit_override: boolean;
  allow_tags_override: boolean;
  allow_vars_override: boolean;
  allow_check_override: boolean;
  allow_diff_override: boolean;
  allow_verbosity_override: boolean;
  webhook_enabled: boolean;
  created_at: string;
  updated_at: string;
}

/** Create/update body. On update omitted fields stay; null clears nullable references. */
export interface TemplateInput {
  name?: string;
  description?: string;
  project_id?: string;
  inventory_id?: string;
  playbook?: string;
  extra_vars?: Record<string, unknown>;
  env_vars?: Record<string, unknown>;
  ssh_secret_id?: string | null;
  become_secret_id?: string | null;
  vault_identity_ids?: string[];
  default_limit?: string;
  default_tags?: string;
  default_skip_tags?: string;
  verbosity?: number;
  check_mode?: boolean;
  diff_mode?: boolean;
  survey?: unknown;
  max_concurrent?: number;
  allow_limit_override?: boolean;
  allow_tags_override?: boolean;
  allow_vars_override?: boolean;
  allow_check_override?: boolean;
  allow_diff_override?: boolean;
  allow_verbosity_override?: boolean;
}

export interface RunParams {
  playbook: string;
  project_revision: string;
  inventory_id: string;
  limit: string;
  /** Effective host set (inventory and limit). */
  hosts: string[] | null;
  tags: string;
  skip_tags: string;
  extra_vars: Record<string, unknown> | null;
  check: boolean;
  diff: boolean;
  verbosity: number;
}

export interface RunPreview {
  host_count: number;
  /** First 50 host names. */
  hosts: string[];
  truncated: boolean;
  all_in_scope: boolean;
}

export interface Run {
  id: string;
  template_id: string | null;
  template_name: string;
  project_id: string | null;
  project_revision: string;
  status: RunStatus;
  trigger: RunTrigger;
  triggered_by: string | null;
  /** Display name (or email) of the user; null for schedule/webhook runs without a user. */
  triggered_by_name: string | null;
  schedule_id: string | null;
  params: RunParams;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  cancel_requested_at: string | null;
  exit_code: number | null;
  hosts_ok: number;
  hosts_changed: number;
  hosts_failed: number;
  hosts_unreachable: number;
}

export interface HostResult {
  host: string;
  ok: number;
  changed: number;
  failed: number;
  unreachable: number;
  skipped: number;
  rescued: number;
  ignored: number;
  /** Names of changed tasks; null when none were recorded. */
  changed_tasks: string[] | null;
}

export interface RunDetail extends Run {
  host_results: HostResult[] | null;
}

/** Payload of the `status` SSE event of a run log stream. */
export interface RunStatusEvent {
  status: RunStatus;
  started_at: string | null;
  finished_at: string | null;
  cancel_requested_at: string | null;
  exit_code: number | null;
  hosts_ok: number;
  hosts_changed: number;
  hosts_failed: number;
  hosts_unreachable: number;
  host_results: HostResult[] | null;
}

export interface RunPage {
  items: Run[] | null;
  next_cursor: string;
}

export interface RunFilter {
  template?: string;
  status?: string;
  trigger?: string;
  user?: string;
  host?: string;
  cursor?: string;
  limit?: number;
}

export type MissedPolicy = 'skip' | 'run_once';

export interface Schedule {
  id: string;
  template_id: string;
  name: string;
  cron: string;
  timezone: string;
  enabled: boolean;
  missed_policy: MissedPolicy;
  overrides: RunOverrides;
  next_run_at: string | null;
  last_run_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface ScheduleInput {
  name?: string;
  cron?: string;
  timezone?: string;
  enabled?: boolean;
  missed_policy?: MissedPolicy;
  overrides?: RunOverrides;
}

/** Template webhook; `secret` is only present once, on enable or rotate. */
export interface TemplateWebhook {
  enabled: boolean;
  has_secret: boolean;
  path: string;
  url?: string;
  secret?: string;
}

export type ConnectorFieldKind = 'string' | 'bool' | 'int';

export interface ConnectorField {
  key: string;
  label: string;
  kind: ConnectorFieldKind;
  required: boolean;
  help?: string;
}

/** Registered connector type with its config schema and setup help. */
export interface ConnectorType {
  type: string;
  label: string;
  /** Accepted secret types; empty when the type needs no credentials. */
  secret_types: SecretType[];
  secret_required: boolean;
  base_url: boolean;
  fields: ConnectorField[];
  help?: string;
}

export interface ConnectorSecretRef {
  id: string;
  name: string;
  type: SecretType;
}

export interface ConnectorCounts {
  networks: number;
  observations: number;
  alerts: number;
  warnings?: string[];
}

export type ConnectorStatus = 'disabled' | 'pending' | 'ok' | 'error';

/** Connector; the credential is only referenced, never returned. */
export interface Connector {
  id: string;
  type: string;
  name: string;
  base_url: string;
  config: Record<string, string | number | boolean>;
  secret: ConnectorSecretRef | null;
  interval_seconds: number;
  enabled: boolean;
  status: ConnectorStatus;
  last_sync_at: string | null;
  last_success_at: string | null;
  next_sync_at: string;
  last_error: string;
  last_duration_ms: number;
  backoff_count: number;
  counts: ConnectorCounts;
  warnings: string[];
  created_at: string;
  updated_at: string;
}

export interface ConnectorInput {
  type: string;
  name: string;
  base_url?: string;
  config?: Record<string, string | number | boolean>;
  secret_id?: string | null;
  interval_seconds?: number;
  enabled?: boolean;
}

/** PATCH body: omitted keys stay, `secret_id: null` removes the credential. */
export type ConnectorUpdate = Partial<Omit<ConnectorInput, 'type'>>;

/** Unsaved configuration to probe (setup wizard "test before save"). */
export interface ConnectorTestInput {
  type: string;
  base_url?: string;
  config?: Record<string, string | number | boolean>;
  secret_id?: string | null;
}

export interface ConnectorTestResult {
  ok: boolean;
  error: string;
}

export interface ConnectorSync {
  id: string;
  trigger: 'schedule' | 'manual';
  status: 'ok' | 'error';
  started_at: string;
  finished_at: string;
  duration_ms: number;
  counts: ConnectorCounts;
  error: string;
}

export interface FleetNetwork {
  id: string;
  connector_id: string;
  connector: string;
  external_key: string;
  cidr: string;
  vlan: number | null;
  interface: string;
  description: string;
  source: string;
  first_seen: string;
  last_seen: string;
}

export interface FleetHost {
  id: string;
  name: string;
  kind: string;
  status: string;
  hostname: string;
  fqdn: string;
  ips: string[];
  macs: string[];
  tags: string[];
  /** Names of the connectors that contributed. */
  connectors: string[];
  open_conflicts: number;
  first_seen: string;
  last_seen: string;
}

export interface FleetSource {
  connector_id: string;
  connector: string;
  type: string;
}

export interface FleetHostFields {
  kind?: string;
  hostname?: string;
  fqdn?: string;
  macs?: string[];
  ips?: string[];
  tags?: string[];
  notes?: string;
  status?: string;
  interface?: string;
  /** Per field: the winning connector (scalars) or all contributors (lists). */
  sources?: Record<string, FleetSource[]>;
}

export interface FleetObservation {
  connector_id: string;
  connector: string;
  connector_type: string;
  external_key: string;
  kind: string;
  fields: {
    macs?: string[];
    hostname?: string;
    fqdn?: string;
    ips?: string[];
    tags?: string[];
    notes?: string;
    status?: string;
    interface?: string;
  };
  first_seen: string;
  last_seen: string;
}

export type ConflictState = 'open' | 'acknowledged' | 'resolved';

export interface FleetConflict {
  id: string;
  type: string;
  state: ConflictState;
  fingerprint: string;
  host_ids: string[];
  details: Record<string, unknown>;
  acknowledged_by: string | null;
  acknowledged_at: string | null;
  first_seen: string;
  last_seen: string;
  resolved_at: string | null;
}

export interface FleetHostDetail extends FleetHost {
  fields: FleetHostFields;
  keys: { kind: 'mac' | 'name' | 'ip'; value: string }[];
  observations: FleetObservation[];
  conflicts: FleetConflict[];
}

export interface FleetHostFilter {
  network?: string;
  q?: string;
  conflicts?: boolean;
}

/** Match kind of a group rule; the set is open (NetBox kinds follow in M6). */
export type GroupRuleKind = 'subnet' | 'proxmox_tag' | (string & {});
export type GroupRuleMode = 'add-only' | 'sync';

export interface GroupRule {
  id: string;
  name: string;
  kind: GroupRuleKind;
  /** Kind specific parameters: {cidr} for subnet, {tag} for proxmox_tag. */
  params: Record<string, unknown>;
  inventory_id: string;
  group: string;
  mode: GroupRuleMode;
  enabled: boolean;
  last_applied_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface GroupRuleInput {
  name: string;
  kind: GroupRuleKind;
  params: Record<string, unknown>;
  inventory_id: string;
  group: string;
  mode?: GroupRuleMode;
  enabled?: boolean;
}

export type GroupRuleUpdate = Partial<GroupRuleInput>;

export interface GroupRuleAdd {
  name: string;
  /** Only set for hosts that are created. */
  ansible_host?: string;
  create_host: boolean;
  fleet_host_id: string;
}

export interface GroupRuleSkipped {
  fleet_host_id: string;
  name: string;
  reason: string;
}

/** The pure diff of a rule against the current inventory state. */
export interface GroupRulePlan {
  group_create: boolean;
  matched: number;
  add: GroupRuleAdd[];
  remove: string[];
  keep: number;
  untrack: string[];
  skipped: GroupRuleSkipped[];
}

export interface GroupRulePreview {
  rule: GroupRule;
  inventory_mode: InventoryMode;
  plan: GroupRulePlan;
}

export interface GroupRuleApplied {
  rule: GroupRule;
  inventory_mode: InventoryMode;
  plan: GroupRulePlan;
  result: {
    group_created: boolean;
    hosts_created: string[];
    added: string[];
    removed: string[];
    version: number;
    /** Set for git inventories when a commit was written. */
    commit: InventoryCommitResult | null;
  };
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
  templates: {
    list: (ws: string) => request<Template[]>('GET', templatesPath(ws)),
    get: (ws: string, id: string) => request<Template>('GET', templatesPath(ws, id)),
    create: (ws: string, t: TemplateInput) => request<Template>('POST', templatesPath(ws), t),
    update: (ws: string, id: string, t: TemplateInput) =>
      request<Template>('PUT', templatesPath(ws, id), t),
    remove: (ws: string, id: string) => request<void>('DELETE', templatesPath(ws, id)),
    start: (ws: string, id: string, ov: RunOverrides) =>
      request<Run>('POST', templatesPath(ws, id, 'runs'), ov),
    previewRun: (ws: string, id: string, ov: RunOverrides) =>
      request<RunPreview>('POST', templatesPath(ws, id, 'runs/preview'), ov),
    webhook: (ws: string, id: string) =>
      request<TemplateWebhook>('GET', templatesPath(ws, id, 'webhook')),
    rotateWebhook: (ws: string, id: string) =>
      request<TemplateWebhook>('POST', templatesPath(ws, id, 'webhook')),
    removeWebhook: (ws: string, id: string) =>
      request<void>('DELETE', templatesPath(ws, id, 'webhook')),
    schedules: (ws: string, id: string) =>
      request<Schedule[]>('GET', templatesPath(ws, id, 'schedules')),
    createSchedule: (ws: string, id: string, s: ScheduleInput) =>
      request<Schedule>('POST', templatesPath(ws, id, 'schedules'), s),
  },
  schedules: {
    update: (ws: string, id: string, s: ScheduleInput) =>
      request<Schedule>('PUT', schedulesPath(ws, id), s),
    remove: (ws: string, id: string) => request<void>('DELETE', schedulesPath(ws, id)),
    preview: (ws: string, cron: string, tz: string, n = 5) =>
      request<{ next_runs: string[] }>(
        'GET',
        schedulesPath(ws, 'preview') +
          `?cron=${encodeURIComponent(cron)}&tz=${encodeURIComponent(tz)}&n=${n}`,
      ),
  },
  runs: {
    list: (ws: string, query: string) =>
      request<RunPage>('GET', runsPath(ws) + (query ? `?${query}` : '')),
    get: (ws: string, id: string) => request<RunDetail>('GET', runsPath(ws, id)),
    cancel: (ws: string, id: string) => request<Run>('POST', runsPath(ws, id, 'cancel')),
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
  connectorTypes: (ws: string) => request<ConnectorType[]>('GET', fleetPath(ws, 'connector-types')),
  connectors: {
    list: (ws: string) => request<Connector[]>('GET', connectorsPath(ws)),
    get: (ws: string, id: string) =>
      request<Connector>('GET', connectorsPath(ws, encodeURIComponent(id))),
    create: (ws: string, c: ConnectorInput) => request<Connector>('POST', connectorsPath(ws), c),
    update: (ws: string, id: string, c: ConnectorUpdate) =>
      request<Connector>('PATCH', connectorsPath(ws, encodeURIComponent(id)), c),
    remove: (ws: string, id: string) =>
      request<void>('DELETE', connectorsPath(ws, encodeURIComponent(id))),
    /** Probes a saved connector (read-only). */
    test: (ws: string, id: string) =>
      request<ConnectorTestResult>('POST', connectorsPath(ws, `${encodeURIComponent(id)}/test`)),
    /** Probes an unsaved configuration. */
    testConfig: (ws: string, c: ConnectorTestInput) =>
      request<ConnectorTestResult>('POST', connectorsPath(ws, 'test'), c),
    /** Queues a sync now; the sync loop picks it up. */
    sync: (ws: string, id: string) =>
      request<{ status: string }>('POST', connectorsPath(ws, `${encodeURIComponent(id)}/sync`)),
    syncs: (ws: string, id: string, limit?: number) =>
      request<ConnectorSync[]>(
        'GET',
        connectorsPath(ws, `${encodeURIComponent(id)}/syncs`) + (limit ? `?limit=${limit}` : ''),
      ),
  },
  groupRules: {
    list: (ws: string) => request<GroupRule[]>('GET', fleetPath(ws, 'group-rules')),
    get: (ws: string, id: string) =>
      request<GroupRule>('GET', fleetPath(ws, `group-rules/${encodeURIComponent(id)}`)),
    create: (ws: string, r: GroupRuleInput) =>
      request<GroupRule>('POST', fleetPath(ws, 'group-rules'), r),
    update: (ws: string, id: string, r: GroupRuleUpdate) =>
      request<GroupRule>('PUT', fleetPath(ws, `group-rules/${encodeURIComponent(id)}`), r),
    remove: (ws: string, id: string) =>
      request<void>('DELETE', fleetPath(ws, `group-rules/${encodeURIComponent(id)}`)),
    /** Dry run: what an apply would add and remove. Nothing is written. */
    preview: (ws: string, id: string) =>
      request<GroupRulePreview>(
        'GET',
        fleetPath(ws, `group-rules/${encodeURIComponent(id)}/preview`),
      ),
    /** Explicit apply; git inventories get a commit on their write branch (409 git_conflict on a moved branch). */
    apply: (ws: string, id: string, inventoryVersion?: number) =>
      request<GroupRuleApplied>(
        'POST',
        fleetPath(ws, `group-rules/${encodeURIComponent(id)}/apply`),
        undefined,
        inventoryVersion,
      ),
  },
  fleet: {
    networks: (ws: string) => request<FleetNetwork[]>('GET', fleetPath(ws, 'networks')),
    hosts: (ws: string, f: FleetHostFilter = {}) => {
      const q = new URLSearchParams();
      if (f.network) q.set('network', f.network);
      if (f.q) q.set('q', f.q);
      if (f.conflicts) q.set('conflicts', 'true');
      const qs = q.toString();
      return request<FleetHost[]>('GET', fleetPath(ws, 'hosts') + (qs ? `?${qs}` : ''));
    },
    host: (ws: string, id: string) =>
      request<FleetHostDetail>('GET', fleetPath(ws, `hosts/${encodeURIComponent(id)}`)),
    conflicts: (ws: string, state?: ConflictState) =>
      request<FleetConflict[]>(
        'GET',
        fleetPath(ws, 'conflicts') + (state ? `?state=${state}` : ''),
      ),
    ackConflict: (ws: string, id: string) =>
      request<FleetConflict>('POST', fleetPath(ws, `conflicts/${encodeURIComponent(id)}/ack`)),
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

function templatesPath(ws: string, id?: string, sub?: string): string {
  let p = `/api/v1/workspaces/${encodeURIComponent(ws)}/templates`;
  if (id) p += `/${encodeURIComponent(id)}`;
  if (sub) p += `/${sub}`;
  return p;
}

function connectorsPath(ws: string, sub?: string): string {
  let p = `/api/v1/workspaces/${encodeURIComponent(ws)}/connectors`;
  if (sub) p += `/${sub}`;
  return p;
}

function fleetPath(ws: string, sub: string): string {
  return `/api/v1/workspaces/${encodeURIComponent(ws)}/${sub}`;
}

function schedulesPath(ws: string, idOrSub: string): string {
  return `/api/v1/workspaces/${encodeURIComponent(ws)}/schedules/${encodeURIComponent(idOrSub)}`;
}

function runsPath(ws: string, id?: string, sub?: string): string {
  let p = `/api/v1/workspaces/${encodeURIComponent(ws)}/runs`;
  if (id) p += `/${encodeURIComponent(id)}`;
  if (sub) p += `/${sub}`;
  return p;
}

/** URL of a run's SSE log stream or log download (`log`, `log.txt`) or the run list stream (`events`). */
export function runStreamUrl(ws: string, kind: 'events' | 'log' | 'log.txt', id?: string): string {
  return kind === 'events' ? runsPath(ws, undefined, 'events') : runsPath(ws, id, kind);
}

/** Login URL for the OIDC redirect flow; the caller must pass a validated path. */
export function oidcLoginUrl(returnTo: string): string {
  return `/api/auth/oidc/login?return_to=${encodeURIComponent(returnTo)}`;
}
