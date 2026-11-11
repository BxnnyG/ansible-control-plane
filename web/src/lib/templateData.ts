import { api, type Inventory, type Project, type SecretMeta, type VaultIdentity } from './api';
import { session } from './session.svelte';

export interface TemplateRefs {
  projects: Project[];
  inventories: Inventory[];
  secrets: SecretMeta[];
  identities: VaultIdentity[];
}

type NameRefs = Pick<TemplateRefs, 'projects' | 'inventories'>;
type CredentialRefs = Pick<TemplateRefs, 'secrets' | 'identities'>;

/** Projects and inventories, enough to show names in a template list. */
export async function loadTemplateNames(ws: string): Promise<NameRefs> {
  const [projects, inventories] = await Promise.all([
    session.can('project.read') ? api.projects.list(ws) : Promise.resolve([]),
    session.can('inventory.read') ? api.inventories.list(ws) : Promise.resolve([]),
  ]);
  return { projects: projects ?? [], inventories: inventories ?? [] };
}

/** Secrets and vault identities, only needed once the template form opens. */
export async function loadTemplateCredentials(ws: string): Promise<CredentialRefs> {
  const [secrets, identities] = await Promise.all([
    session.can('secrets.meta.read') ? api.secrets.list(ws) : Promise.resolve([]),
    session.can('vault.read') ? api.vaultIdentities.list(ws) : Promise.resolve([]),
  ]);
  return { secrets: secrets ?? [], identities: identities ?? [] };
}

/** Loads the objects a template references; lists the caller may not read stay empty. */
export async function loadTemplateRefs(ws: string): Promise<TemplateRefs> {
  const [names, creds] = await Promise.all([loadTemplateNames(ws), loadTemplateCredentials(ws)]);
  return { ...names, ...creds };
}
