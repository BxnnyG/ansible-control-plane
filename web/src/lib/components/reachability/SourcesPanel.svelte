<script lang="ts">
  import { api, type Inventory, type InventoryHost, type ReachabilitySource } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btnDanger, btnPrimary, label } from '$lib/ui';
  import { toasts } from '$lib/toasts.svelte';
  import Combobox from '$lib/components/Combobox.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    sources,
    canWrite,
    onchange,
  }: { ws: string; sources: ReachabilitySource[]; canWrite: boolean; onchange: () => void } =
    $props();

  let inventories = $state<Inventory[]>([]);
  let hosts = $state<InventoryHost[]>([]);
  let inventoryId = $state('');
  let hostId = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    if (!canWrite) return;
    api.inventories
      .list(ws)
      .then((i) => (inventories = i ?? []))
      .catch((e) => (error = describeError(e)));
  });

  $effect(() => {
    const inv = inventoryId;
    hostId = '';
    hosts = [];
    if (!inv) return;
    api.inventories
      .hosts(ws, inv)
      .then((h) => (hosts = h ?? []))
      .catch((e) => (error = describeError(e)));
  });

  const invOptions = $derived(inventories.map((i) => ({ value: i.id, label: i.name })));
  const hostOptions = $derived(
    hosts
      .filter((h) => !sources.some((s) => s.host_id === h.id))
      .map((h) => ({ value: h.id, label: h.name })),
  );

  async function add() {
    if (!hostId) return;
    busy = true;
    error = null;
    try {
      await api.reachability.addSource(ws, hostId);
      hostId = '';
      onchange();
    } catch (e) {
      error = describeError(e);
    } finally {
      busy = false;
    }
  }

  async function remove(s: ReachabilitySource) {
    try {
      await api.reachability.removeSource(ws, s.id);
      toasts.success(`${s.host_name} removed.`);
      onchange();
    } catch (e) {
      error = describeError(e);
    }
  }
</script>

<div class="space-y-4 p-5">
  <FormError id="reach-sources-error" message={error} />
  {#if sources.length === 0}
    <p class="text-sm text-muted">No source hosts yet. Sources run the probes.</p>
  {:else}
    <ul class="divide-y divide-border text-sm">
      {#each sources as s (s.id)}
        <li class="flex items-center justify-between gap-3 py-1.5">
          <span class="min-w-0 truncate">{s.host_name}</span>
          {#if canWrite}
            <button class={btnDanger} onclick={() => remove(s)} aria-label="Remove {s.host_name}"
              >Remove</button
            >
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
  {#if canWrite}
    <div class="grid gap-3 md:grid-cols-[1fr_1fr_auto] md:items-end">
      <div>
        <label class={label} for="reach-src-inv">Inventory</label>
        <Combobox id="reach-src-inv" options={invOptions} bind:value={inventoryId} />
      </div>
      <div>
        <label class={label} for="reach-src-host">Host</label>
        <Combobox
          id="reach-src-host"
          options={hostOptions}
          bind:value={hostId}
          disabled={!inventoryId}
        />
      </div>
      <button class={btnPrimary} disabled={!hostId || busy} onclick={add}>Add source</button>
    </div>
  {/if}
</div>
