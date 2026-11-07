/**
 * Typed client for the backend API. The wire types are generated from
 * api/openapi.yaml into ./api/schema.d.ts (`npm run gen:api`); the aliases below
 * keep the short names used by the UI. Do not hand-write response types here:
 * change the spec and regenerate.
 */
import type { components } from './api/schema';

type S = components['schemas'];

export type AuthConfig = S['AuthConfig'];
export type Membership = S['Membership'];
export type Me = S['Me'];
export type WorkspaceDetail = S['WorkspaceDetail'];
export type RoleMapping = S['RoleMapping'];
export type SetupStatus = S['SetupStatus'];
export type BreakGlassCredentials = S['BreakGlassCredentials'];
export type SecretType = S['SecretType'];
export type SecretMeta = S['SecretMeta'];
export type SecretCreate = S['SecretCreate'];
export type SecretUpdate = S['SecretUpdate'];
export type ProjectSyncStatus = S['ProjectSyncStatus'];
export type Project = S['Project'];
export type WebhookProvider = S['WebhookProvider'];
export type ProjectWebhook = S['ProjectWebhook'];
export type ProjectInput = S['ProjectInput'];
export type ProjectUpdate = S['ProjectUpdate'];
export type HostKeyInfo = S['HostKeyInfo'];
export type ProjectRevision = S['ProjectRevision'];
export type InventoryMode = S['InventoryMode'];
export type Inventory = S['Inventory'];
export type InventoryInput = S['InventoryInput'];
export type InventoryUpdate = S['InventoryUpdate'];
export type InventoryGroup = S['InventoryGroup'];
export type InventoryGroupInput = S['InventoryGroupInput'];
export type InventoryGroupUpdate = S['InventoryGroupUpdate'];
export type InventoryHost = S['InventoryHost'];
export type InventoryHostInput = S['InventoryHostInput'];
export type InventoryHostUpdate = S['InventoryHostUpdate'];
export type InventoryImportInput = S['InventoryImportInput'];
export type InventoryImportResult = S['InventoryImportResult'];
export type InventoryRender = S['InventoryRender'];
export type InventoryDiff = S['InventoryDiff'];
export type InventoryCommitResult = S['InventoryCommitResult'];
export type VaultProvider = S['VaultProvider'];
export type VaultIdentity = S['VaultIdentity'];
export type VaultIdentityInput = S['VaultIdentityInput'];
export type VaultListing = S['VaultListing'];
export type VaultDecrypted = S['VaultDecrypted'];
export type VaultEncryptInput = S['VaultEncryptInput'];
export type VaultRekeyInput = S['VaultRekeyInput'];
export type VaultWriteResult = S['VaultWriteResult'];
export type RunStatus = S['RunStatus'];
export type RunTrigger = S['RunTrigger'];
export type RunOverrides = S['RunOverrides'];
export type Template = S['Template'];
export type TemplateInput = S['TemplateInput'];
export type RunParams = S['RunParams'];
export type RunPreview = S['RunPreview'];
export type Run = S['Run'];
export type HostResult = S['HostResult'];
export type RunDetail = S['RunDetail'];
export type RunStatusEvent = S['RunStatusEvent'];
export type RunPage = S['RunPage'];
export type MissedPolicy = S['MissedPolicy'];
export type Schedule = S['Schedule'];
export type ScheduleInput = S['ScheduleInput'];
export type TemplateWebhook = S['TemplateWebhook'];
export type ConnectorFieldKind = S['ConnectorFieldKind'];
export type ConnectorField = S['ConnectorField'];
export type ConnectorType = S['ConnectorType'];
export type ConnectorSecretRef = S['ConnectorSecretRef'];
export type ConnectorCounts = S['ConnectorCounts'];
export type ConnectorStatus = S['ConnectorStatus'];
export type Connector = S['Connector'];
export type ConnectorCreated = S['ConnectorCreated'];
export type ConnectorInput = S['ConnectorInput'];
export type ConnectorUpdate = S['ConnectorUpdate'];
export type ConnectorTestInput = S['ConnectorTestInput'];
export type ConnectorTestResult = S['ConnectorTestResult'];
export type ConnectorSync = S['ConnectorSync'];
export type FleetNetwork = S['FleetNetwork'];
export type FleetNetworkCounts = S['FleetNetworkCounts'];
export type FleetAnsibleLink = S['FleetAnsibleLink'];
export type FleetRunRef = S['FleetRunRef'];
export type FleetDrift = S['FleetDrift'];
export type FleetHost = S['FleetHost'];
export type FleetAddInput = S['FleetAddInput'];
export type FleetAddResult = S['FleetAddResult'];
export type AlertSeverity = S['AlertSeverity'];
export type FleetAlert = S['FleetAlert'];
export type FleetSource = S['FleetSource'];
export type FleetHostFields = S['FleetHostFields'];
export type FleetObservation = S['FleetObservation'];
export type ConflictState = S['ConflictState'];
export type FleetConflict = S['FleetConflict'];
export type FleetHostDetail = S['FleetHostDetail'];
export type GroupRuleKind = S['GroupRuleKind'];
export type GroupRuleMode = S['GroupRuleMode'];
export type GroupRule = S['GroupRule'];
export type GroupRuleInput = S['GroupRuleInput'];
export type GroupRuleUpdate = S['GroupRuleUpdate'];
export type GroupRuleAdd = S['GroupRuleAdd'];
export type GroupRuleSkipped = S['GroupRuleSkipped'];
export type GroupRulePlan = S['GroupRulePlan'];
export type GroupRulePreview = S['GroupRulePreview'];
export type RemediationTarget = S['RemediationTarget'];
export type RemediationRule = S['RemediationRule'];
export type RemediationRuleInput = S['RemediationRuleInput'];
export type RemediationRef = S['RemediationRef'];
export type RemediationResult = S['RemediationResult'];
export type BulkRunSkipped = S['BulkRunSkipped'];
export type BulkRunResult = S['BulkRunResult'];
export type GroupRuleApplied = S['GroupRuleApplied'];
export type FleetOnline = FleetHost['online'];
export type FleetRunResult = FleetRunRef['result'];
export type VaultWriteOptions = Pick<VaultEncryptInput, 'branch' | 'message'>;

/** Machine-readable error codes of the projects API (`code` of the error envelope). */
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

/** Machine-readable error codes of the inventories API (`code` of the error envelope). */
export type InventoryErrorCode =
  | 'not_found'
  | 'conflict'
  | 'invalid'
  | 'bad_request'
  | 'version_conflict'
  | 'git_conflict'
  | 'vault_requires_yaml'
  | 'project_not_synced';

export interface RunFilter {
  template?: string;
  status?: string;
  trigger?: string;
  user?: string;
  host?: string;
  cursor?: string;
  limit?: number;
}

export interface FleetAlertFilter {
  state?: 'open' | 'resolved';
  host_id?: string;
  connector?: string;
}

export interface FleetHostFilter {
  network?: string;
  q?: string;
  conflicts?: boolean;
  alerts?: boolean;
  /** false lists the unmanaged hosts (not linked to any inventory host). */
  managed?: boolean;
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
    create: (ws: string, c: ConnectorInput) =>
      request<ConnectorCreated>('POST', connectorsPath(ws), c),
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
  remediation: {
    list: (ws: string) => request<RemediationRule[]>('GET', fleetPath(ws, 'remediation-rules')),
    create: (ws: string, r: RemediationRuleInput) =>
      request<RemediationRule>('POST', fleetPath(ws, 'remediation-rules'), r),
    update: (ws: string, id: string, r: RemediationRuleInput) =>
      request<RemediationRule>(
        'PUT',
        fleetPath(ws, `remediation-rules/${encodeURIComponent(id)}`),
        r,
      ),
    remove: (ws: string, id: string) =>
      request<void>('DELETE', fleetPath(ws, `remediation-rules/${encodeURIComponent(id)}`)),
    /** Open alerts an (unsaved) rule would match: the regex test of the editor. */
    preview: (
      ws: string,
      c: Pick<
        RemediationRuleInput,
        'connector_type' | 'service_regex' | 'summary_regex' | 'min_severity'
      >,
    ) => request<FleetAlert[]>('POST', fleetPath(ws, 'remediation-rules/preview'), c),
    /** Applicable rules of every open alert the caller sees, keyed by alert id. */
    applicable: (ws: string) =>
      request<Record<string, RemediationRef[]>>(
        'GET',
        fleetPath(ws, 'remediation-rules/applicable'),
      ),
    /** Starts the fix of an alert (a check run first when the rule asks for it and skipCheck is false). */
    remediate: (ws: string, alertId: string, ruleId: string, skipCheck = false) =>
      request<RemediationResult>(
        'POST',
        fleetPath(ws, `alerts/${encodeURIComponent(alertId)}/remediate`),
        { rule_id: ruleId, skip_check: skipCheck },
      ),
  },
  fleet: {
    bulkRun: (ws: string, hostIds: string[], templateId: string, checkMode: boolean) =>
      request<BulkRunResult>('POST', fleetPath(ws, 'fleet/bulk-run'), {
        host_ids: hostIds,
        template_id: templateId,
        check_mode: checkMode,
      }),
    networks: (ws: string) => request<FleetNetwork[]>('GET', fleetPath(ws, 'networks')),
    hosts: (ws: string, f: FleetHostFilter = {}) => {
      const q = new URLSearchParams();
      if (f.network) q.set('network', f.network);
      if (f.q) q.set('q', f.q);
      if (f.conflicts) q.set('conflicts', 'true');
      if (f.alerts) q.set('alerts', 'true');
      if (f.managed !== undefined) q.set('managed', String(f.managed));
      const qs = q.toString();
      return request<FleetHost[]>('GET', fleetPath(ws, 'hosts') + (qs ? `?${qs}` : ''));
    },
    host: (ws: string, id: string) =>
      request<FleetHostDetail>('GET', fleetPath(ws, `hosts/${encodeURIComponent(id)}`)),
    /** Puts an unmanaged host into an inventory group (git inventories get a commit). */
    addToInventory: (ws: string, id: string, i: FleetAddInput, inventoryVersion?: number) =>
      request<FleetAddResult>(
        'POST',
        fleetPath(ws, `hosts/${encodeURIComponent(id)}/add-to-inventory`),
        i,
        inventoryVersion,
      ),
    alerts: (ws: string, f: FleetAlertFilter = {}) => {
      const q = new URLSearchParams();
      if (f.state) q.set('state', f.state);
      if (f.host_id) q.set('host_id', f.host_id);
      if (f.connector) q.set('connector', f.connector);
      const qs = q.toString();
      return request<FleetAlert[]>('GET', fleetPath(ws, 'alerts') + (qs ? `?${qs}` : ''));
    },
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
