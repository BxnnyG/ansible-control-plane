<script lang="ts">
  import {
    api,
    ApiError,
    type Inventory,
    type InventoryImportResult,
    type Project,
  } from '$lib/api';
  import { normalizeScan, type ScanResult } from '$lib/scan';
  import { btn, btnPrimary, card, input, label } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';

  let {
    ws,
    inv,
    projects,
    onchanged,
  }: { ws: string; inv: Inventory; projects: Project[]; onchanged: () => void } = $props();

  // svelte-ignore state_referenced_locally
  let projectId = $state(inv.project_id ?? '');
  // svelte-ignore state_referenced_locally
  let path = $state(inv.path);
  let scan = $state<ScanResult | null>(null);
  let result = $state<InventoryImportResult | null>(null);
  let error = $state<unknown>(null);
  let busy = $state(false);

  const candidates = $derived(scan?.inventories ?? []);
  const format = $derived(candidates.find((c) => c.path === path)?.format);

  async function loadScan(pid: string) {
    scan = null;
    if (!pid) return;
    try {
      scan = normalizeScan((await api.projects.latestRevision(ws, pid)).scan);
    } catch (e) {
      // 404: never synced, so there is nothing to pick from.
      if (!(e instanceof ApiError && e.status === 404)) error = e;
    }
  }

  $effect(() => {
    void loadScan(projectId);
  });

  async function run() {
    busy = true;
    error = null;
    result = null;
    try {
      result = await api.inventories.import(
        ws,
        inv.id,
        {
          project_id: projectId || undefined,
          path: path || undefined,
          ...(format === 'ini' || format === 'yaml' ? { format } : {}),
        },
        inv.version,
      );
      onchanged();
    } catch (e) {
      error = e;
    } finally {
      busy = false;
    }
  }
</script>

<section class={card} aria-labelledby="import-h">
  <h2 id="import-h" class="mb-2 text-sm font-semibold">Import from project</h2>
  <p class="mb-3 text-sm text-muted">
    Replaces hosts and groups with the content of an inventory file from a synced project.
  </p>
  <div class="grid gap-3 sm:grid-cols-2">
    <div>
      <label class={label} for="imp-project">Project</label>
      <select id="imp-project" class={input} bind:value={projectId}>
        <option value="">Select a project</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="imp-path">Inventory path</label>
      {#if candidates.length > 0}
        <select id="imp-path" class={input} bind:value={path}>
          <option value="">Select a path</option>
          {#each candidates as c (c.path)}<option value={c.path}>{c.path} ({c.format})</option
            >{/each}
        </select>
      {:else}
        <input
          id="imp-path"
          class={input}
          bind:value={path}
          placeholder="inventories/prod/hosts.yml"
        />
        {#if projectId}
          <p class="mt-1 text-xs text-muted">No inventories found in the latest scan.</p>
        {/if}
      {/if}
    </div>
  </div>
  <div class="mt-3">
    <button class={btnPrimary} onclick={run} disabled={busy || !projectId || !path}>Import</button>
    {#if inv.mode === 'git' && inv.project_id}
      <button
        class="{btn} ml-2"
        onclick={() => {
          projectId = inv.project_id ?? '';
          path = inv.path;
        }}
      >
        Use stored source
      </button>
    {/if}
  </div>
  {#if result}
    <p role="status" class="mt-3 text-sm text-ok">
      Imported {result.hosts} hosts and {result.groups} groups from
      <code>{result.sha.slice(0, 8)}</code>.
    </p>
    {#if result.warnings.length > 0}
      <ul class="mt-1 list-inside list-disc text-sm text-warn">
        {#each result.warnings as w, i (i)}<li>{w}</li>{/each}
      </ul>
    {/if}
  {/if}
  <ErrorBox {error} />
</section>
