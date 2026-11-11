<script lang="ts">
  import type { FleetNetwork } from '$lib/api';
  import {
    counter,
    filterNetworks,
    groupByConnector,
    groupState,
    hideEmpty,
    initialSelection,
    MANY_NETWORKS,
    sortNetworks,
    type NetworkSort,
    invertSelection,
    selectAll,
    selectNone,
    toggleGroup,
    type Selection,
  } from '$lib/networkPicker';
  import { btn, input } from '$lib/ui';

  let {
    networks,
    selection = $bindable(),
    disabled = false,
  }: { networks: FleetNetwork[]; selection: Selection; disabled?: boolean } = $props();

  let query = $state('');
  let sort = $state<NetworkSort>('default');
  // svelte-ignore state_referenced_locally
  let hideEmptyOn = $state(networks.length > MANY_NETWORKS);
  let collapsed = $state<Record<string, boolean>>({});
  // Order and visibility follow the stored tracked flags, not the live
  // selection, so rows do not jump around while the user ticks boxes.
  const stored = $derived(initialSelection(networks));
  const emptyCount = $derived(networks.filter((n) => n.host_count === 0 && !stored[n.id]).length);
  const visible = $derived(hideEmptyOn ? hideEmpty(networks, stored) : networks);
  const filtered = $derived(filterNetworks(sortNetworks(visible, stored, sort), query));
  const groups = $derived(groupByConnector(filtered));
  const count = $derived(counter(networks, selection));
</script>

<div class="space-y-3">
  <div class="flex flex-wrap items-center gap-2">
    <input
      type="search"
      aria-label="Filter networks"
      placeholder="Filter by CIDR, description, VLAN, connector"
      class="{input} max-w-xs"
      bind:value={query}
    />
    <button
      type="button"
      class={btn}
      {disabled}
      onclick={() => (selection = selectAll(selection, filtered))}>Select all</button
    >
    <button
      type="button"
      class={btn}
      {disabled}
      onclick={() => (selection = selectNone(selection, filtered))}>Select none</button
    >
    <button
      type="button"
      class={btn}
      {disabled}
      onclick={() => (selection = invertSelection(selection, filtered))}>Invert</button
    >
    <label class="flex items-center gap-1.5 text-sm">
      Sort
      <select class="{input} !w-auto" aria-label="Sort networks" bind:value={sort}>
        <option value="default">Tracked first, then CIDR</option>
        <option value="hosts">Tracked first, then host count</option>
      </select>
    </label>
    <label class="flex items-center gap-1.5 text-sm">
      <input type="checkbox" bind:checked={hideEmptyOn} />
      Hide empty networks{emptyCount > 0 ? ` (${emptyCount})` : ''}
    </label>
    <span class="text-sm text-muted" aria-live="polite"
      >{count.selected} of {count.total} selected</span
    >
  </div>

  {#if groups.length === 0}
    <p class="text-sm text-muted">No network matches the filter.</p>
  {:else}
    <div class="max-h-[60vh] space-y-3 overflow-auto rounded-lg border border-border p-3">
      {#each groups as g (g.connector)}
        {@const state = groupState(g.items, selection)}
        <fieldset class="space-y-1">
          <legend class="w-full">
            <label class="inline-flex items-center gap-2 text-sm font-medium">
              <input
                type="checkbox"
                {disabled}
                checked={state === 'all'}
                indeterminate={state === 'some'}
                aria-label="Select all networks of {g.connector}"
                onchange={() => (selection = toggleGroup(selection, g.items))}
              />
              {g.connector}
              <span class="font-normal text-muted">({g.items.length})</span>
            </label>
            <button
              type="button"
              class="ml-6 text-xs text-muted underline"
              aria-expanded={!collapsed[g.connector]}
              onclick={() => (collapsed[g.connector] = !collapsed[g.connector])}
              >{collapsed[g.connector] ? 'Show' : 'Hide'} list</button
            >
          </legend>
          {#each collapsed[g.connector] ? [] : g.items as n (n.id)}
            <label class="ml-6 flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                bind:checked={selection[n.id]}
                {disabled}
                aria-label="Track {n.cidr}"
              />
              <span class="font-mono">{n.cidr}</span>
              <span class="text-muted">
                {n.description || n.interface || '-'}{n.vlan ? ` - VLAN ${n.vlan}` : ''} - {n.host_count}
                host{n.host_count === 1 ? '' : 's'}
              </span>
            </label>
          {/each}
        </fieldset>
      {/each}
    </div>
  {/if}
</div>
