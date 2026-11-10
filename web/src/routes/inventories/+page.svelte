<script lang="ts">
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { goto } from '$app/navigation';
  import { api, type Inventory, type InventoryMode, type Project } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { btn, btnPrimary, card, fmtDate, input, label, td, th } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';

  let items = $state<Inventory[]>([]);
  let projects = $state<Project[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let creating = $state(false);

  let name = $state('');
  let mode = $state<InventoryMode>('db');
  let projectId = $state('');
  let path = $state('');
  let branch = $state('');

  const canRead = $derived(session.can('inventory.read'));
  const canWrite = $derived(session.can('inventory.write'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      items = await api.inventories.list(ws);
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
    if (!session.slug) return;
    error = null;
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
    }
  }
</script>

<svelte:head><title>Inventories - Ansible Control Plane</title></svelte:head>

<header
  class="mb-5 flex flex-wrap items-end justify-between gap-x-4 gap-y-3 border-b border-border pb-4"
>
  <div>
    <h1 class="text-xl font-semibold">Inventories</h1>
    <p class="mt-1 text-muted">Host and group definitions stored in the database or in Git.</p>
  </div>
  {#if canWrite && !creating}
    <button class={btnPrimary} onclick={() => (creating = true)}>New inventory</button>
  {/if}
</header>

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
        <button class={btnPrimary}>Create</button>
        <button type="button" class={btn} onclick={() => (creating = false)}>Cancel</button>
      </div>
    </form>
  {/if}
  <ErrorBox {error} />

  {#if loading}
    <ListSkeleton />
  {:else if items.length === 0}
    <div class="empty">No inventories yet.</div>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Mode</th><th class={th}>Hosts</th>
            <th class={th}>Groups</th><th class={th}>Last import</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as i (i.id)}
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
