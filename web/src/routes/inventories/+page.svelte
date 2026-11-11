<script lang="ts">
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import SortHeader from '$lib/components/SortHeader.svelte';
  import { sortRows, type SortState } from '$lib/sort';
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { goto } from '$app/navigation';
  import { api, type Inventory, type InventoryMode, type Project } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { btn, btnPrimary, card, fmtDate, input, label, td } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';

  let items = $state<Inventory[]>([]);
  let projects = $state<Project[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let creating = $state(false);
  let saving = $state(false);

  let name = $state('');
  let mode = $state<InventoryMode>('db');
  let projectId = $state('');
  let path = $state('');
  let branch = $state('');

  let sort = $state<SortState | null>(null);
  const rows = $derived(
    sortRows(items, sort, {
      name: (i) => i.name,
      mode: (i) => i.mode,
      hosts: (i) => i.hosts,
      groups: (i) => i.groups,
      import: (i) => i.last_import_at,
    }),
  );

  const canRead = $derived(session.can('inventory.read'));
  const canWrite = $derived(session.can('inventory.write'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      items = await api.inventories.list(ws, (d) => {
        if (session.slug === ws) items = d;
      });
      projects = session.can('project.read') ? await api.projects.list(ws) : [];
    } catch (e) {
      error = e;
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    const ws = session.slug;
    if (ws && canRead) untrack(() => void load(ws));
    else loading = false;
  });

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!session.slug || saving) return;
    error = null;
    saving = true;
    try {
      const inv = await api.inventories.create(session.slug, {
        name,
        mode,
        ...(mode === 'git'
          ? { project_id: projectId, path, ...(branch ? { write_branch: branch } : {}) }
          : {}),
      });
      await goto(`/inventories/${inv.id}`);
    } catch (err) {
      error = err;
    } finally {
      saving = false;
    }
  }
</script>

<svelte:head><title>Inventories - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Inventories"
  description="Host and group definitions stored in the database or in Git."
>
  {#snippet actions()}
    {#if canWrite && !creating}
      <button class={btnPrimary} onclick={() => (creating = true)}>New inventory</button>
    {/if}
  {/snippet}
</PageHeader>

{#if !canRead}
  <p class="text-muted">You do not have permission to view inventories in this workspace.</p>
{:else}
  {#if creating}
    <form onsubmit={create} class="{card} mb-6 max-w-xl space-y-3" aria-label="New inventory">
      <div>
        <label class={label} for="i-name">Name</label>
        <input id="i-name" class={input} bind:value={name} required />
      </div>
      <div>
        <label class={label} for="i-mode">Mode</label>
        <select id="i-mode" class={input} bind:value={mode}>
          <option value="db">Database (edited here)</option>
          <option value="git">Git (file in a project)</option>
        </select>
      </div>
      {#if mode === 'git'}
        <div>
          <label class={label} for="i-project">Project</label>
          <select id="i-project" class={input} bind:value={projectId} required>
            <option value="">Select a project</option>
            {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
          </select>
        </div>
        <div>
          <label class={label} for="i-path">Inventory path</label>
          <input
            id="i-path"
            class={input}
            bind:value={path}
            required
            placeholder="inventories/hosts.yml"
          />
        </div>
        <div>
          <label class={label} for="i-branch">Write branch (optional)</label>
          <input id="i-branch" class={input} bind:value={branch} placeholder="acp/inventory" />
        </div>
      {/if}
      <div class="flex gap-2">
        <button class={btnPrimary} disabled={saving}>{saving ? 'Creating...' : 'Create'}</button>
        <button type="button" class={btn} onclick={() => (creating = false)}>Cancel</button>
      </div>
    </form>
  {/if}
  <ErrorBox {error} />
  {#if error}<button type="button" class={btn} onclick={() => session.slug && load(session.slug)}
      >Retry</button
    >{/if}

  {#if loading}
    <ListSkeleton />
  {:else if items.length === 0}
    <EmptyState
      title="No inventories yet."
      hint="An inventory lists the hosts and groups a template runs against."
    >
      {#if canWrite}
        <button class={btnPrimary} onclick={() => (creating = true)}>New inventory</button>
      {/if}
    </EmptyState>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <SortHeader label="Name" key="name" bind:sort /><SortHeader
              label="Mode"
              key="mode"
              bind:sort
            /><SortHeader label="Hosts" key="hosts" bind:sort />
            <SortHeader label="Groups" key="groups" bind:sort /><SortHeader
              label="Last import"
              key="import"
              bind:sort
            />
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each rows as i (i.id)}
            <tr>
              <td class={td}
                ><a
                  class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                  href="/inventories/{i.id}">{i.name}</a
                ></td
              >
              <td class={td}>{i.mode}</td>
              <td class={td}>{i.hosts}</td>
              <td class={td}>{i.groups}</td>
              <td class={td}>{fmtDate(i.last_import_at)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
