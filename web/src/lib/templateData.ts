import { api, type Inventory, type Project, type SecretMeta, type VaultIdentity } from './api';
import { session } from './session.svelte';

export interface TemplateRefs {
  projects: Project[];
  inventories: Inventory[];
  secrets: SecretMeta[];
  identities: VaultIdentity[];
}

/** Loads the objects a template references; lists the caller may not read stay empty. */
export async function loadTemplateRefs(ws: string): Promise<TemplateRefs> {
  const [projects, inventories, secrets, identities] = await Promise.all([
    session.can('project.read') ? api.projects.list(ws) : Promise.resolve([]),
    session.can('inventory.read') ? api.inventories.list(ws) : Promise.resolve([]),
    session.can('secrets.meta.read') ? api.secrets.list(ws) : Promise.resolve([]),
    session.can('vault.read') ? api.vaultIdentities.list(ws) : Promise.resolve([]),
  ]);
  return {
    projects: projects ?? [],
    inventories: inventories ?? [],
    secrets: secrets ?? [],
    identities: identities ?? [],
  };
}
