<script lang="ts">
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { toasts } from '$lib/toasts.svelte';
  import Breadcrumbs from '$lib/components/Breadcrumbs.svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import {
    api,
    type Inventory,
    type InventoryGroup,
    type InventoryGroupInput,
    type InventoryHost,
    type InventoryHostInput,
    type Project,
  } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, fmtDate } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';
  import GitPanel from '$lib/components/inventories/GitPanel.svelte';
  import GroupsPanel from '$lib/components/inventories/GroupsPanel.svelte';
  import HostsPanel from '$lib/components/inventories/HostsPanel.svelte';
  import ImportPanel from '$lib/components/inventories/ImportPanel.svelte';
  import RenderPanel from '$lib/components/inventories/RenderPanel.svelte';

  const id = $derived(page.params.id ?? '');
  const ws = $derived(session.slug);
  const canWrite = $derived(session.can('inventory.write'));

  let inv = $state<Inventory | null>(null);
  let groups = $state<InventoryGroup[]>([]);
  let hosts = $state<InventoryHost[]>([]);
  let projects = $state<Project[]>([]);
  let error = $state<unknown>(null);
  let confirmDelete = $state(false);

  async function load(slug: string, iid: string) {
    try {
      [inv, groups, hosts] = await Promise.all([
        api.inventories.get(slug, iid),
        api.inventories.groups(slug, iid),
        api.inventories.hosts(slug, iid),
      ]);
      if (projects.length === 0 && session.can('project.read')) {
        projects = await api.projects.list(slug);
      }
    } catch (e) {
      error = e;
    }
  }

  $effect(() => {
    if (ws && id) void load(ws, id);
  });

  /** Runs a mutation with the current version, reloads on success. Returns success. */
  async function mutate(fn: (slug: string, version: number) => Promise<unknown>): Promise<boolean> {
    if (!ws || !inv) return false;
    error = null;
    try {
      await fn(ws, inv.version);
      await load(ws, id);
      return true;
    } catch (e) {
      error = e;
      return false;
    }
  }

  const saveGroup = (gid: string | null, g: InventoryGroupInput) =>
    mutate((s, v) =>
      gid
        ? api.inventories.updateGroup(s, id, gid, g, v)
        : api.inventories.createGroup(s, id, g, v),
    );
  const saveHost = (hid: string | null, h: InventoryHostInput) =>
    mutate((s, v) =>
      hid ? api.inventories.updateHost(s, id, hid, h, v) : api.inventories.createHost(s, id, h, v),
    );
  const removeGroup = async (g: InventoryGroup) => {
    await mutate((s, v) => api.inventories.removeGroup(s, id, g.id, v));
  };
  const removeHost = async (h: InventoryHost) => {
    if (await mutate((s, v) => api.inventories.removeHost(s, id, h.id, v)))
      toasts.success('Host deleted.');
  };

  async function remove() {
    if (!ws) return;
    try {
      await api.inventories.remove(ws, id);
      toasts.success('Inventory deleted.');
      await goto('/inventories');
    } catch (e) {
      error = e;
      confirmDelete = false;
    }
  }
</script>

<svelte:head><title>{inv?.name ?? 'Inventory'} - Ansible Control Plane</title></svelte:head>

<Breadcrumbs trail={[{ label: 'Inventories', href: '/inventories' }]} />

{#if inv && ws}
  <header
    class="mb-5 flex flex-wrap items-end justify-between gap-x-4 gap-y-3 border-b border-border pb-4"
  >
    <div>
      <h1 class="text-xl font-semibold">{inv.name}</h1>
      <p class="mt-1 text-sm text-muted">
        Mode: {inv.mode}{inv.mode === 'git' ? ` (${inv.path})` : ''} - version {inv.version} - last import
        {fmtDate(inv.last_import_at)}
      </p>
    </div>
    <div class="flex gap-2">
      <button class={btn} onclick={() => load(ws, id)}>Reload</button>
      {#if canWrite}
        <button class={btnDanger} onclick={() => (confirmDelete = true)}>Delete</button>
      {/if}
    </div>
  </header>

  {#if confirmDelete}
    <ConfirmDialog
      title="Confirm delete"
      onconfirm={remove}
      oncancel={() => (confirmDelete = false)}
    >
      <p>Delete inventory <strong>{inv.name}</strong>?</p>
    </ConfirmDialog>
  {/if}

  <ErrorBox {error} />

  <div class="space-y-6">
    <GroupsPanel {groups} {hosts} {canWrite} save={saveGroup} remove={removeGroup} />
    <HostsPanel {hosts} {groups} {canWrite} save={saveHost} remove={removeHost} />
  </div>

  <div class="mt-6 space-y-6">
    {#if canWrite}
      <ImportPanel {ws} {inv} {projects} onchanged={() => load(ws, id)} />
    {/if}
    {#if inv.mode === 'git'}
      <GitPanel {ws} {inv} {canWrite} onchanged={() => load(ws, id)} />
    {/if}
    <RenderPanel {ws} {id} />
  </div>
{:else if error}
  <ErrorBox {error} />
{:else}
  <ListSkeleton />
{/if}
