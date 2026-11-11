<script lang="ts">
  import { untrack } from 'svelte';
  import { api, type FleetNetwork } from '$lib/api';
  import { describeError } from '$lib/errors';
  import {
    initialSelection,
    isDirty,
    networkChipLabel,
    selectedNetworkIds,
  } from '$lib/networkPicker';
  import { session } from '$lib/session.svelte';
  import { btn, btnPrimary } from '$lib/ui';
  import NetworkPicker from '$lib/components/fleet/NetworkPicker.svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    networks: given,
    saveLabel = 'Save',
    allowUnchanged = false,
    collapsible = false,
    onsaved,
  }: {
    ws: string;
    /** Already loaded networks; the panel fetches them itself when omitted. */
    networks?: FleetNetwork[];
    saveLabel?: string;
    /** Keeps Save enabled without changes (setup wizard: Save continues). */
    allowUnchanged?: boolean;
    /** Starts as a chip summary of the tracked networks with an Edit button. */
    collapsible?: boolean;
    onsaved?: () => void;
  } = $props();

  let editing = $state(false);
  const showPicker = $derived(!collapsible || editing);

  function cancel() {
    selection = initialSelection(networks);
    newTracked = savedNewTracked;
    editing = false;
  }

  let networks = $state<FleetNetwork[]>([]);
  const trackedNets = $derived(networks.filter((n) => n.tracked));
  let selection = $state<Record<string, boolean>>({});
  let newTracked = $state(true);
  let savedNewTracked = $state(true);
  let loading = $state(true);
  let saving = $state(false);
  let saved = $state(false);
  let error = $state<string | null>(null);
  let runId = 0;

  const canManage = $derived(session.can('fleet.manage'));
  const dirty = $derived(isDirty(networks, selection) || newTracked !== savedNewTracked);

  $effect(() => {
    const slug = ws;
    const list = given;
    untrack(() => {
      // Only the newest run may write: a slower, older run would otherwise
      // overwrite the selection with stale tracked flags.
      const run = ++runId;
      loading = true;
      Promise.all([
        list ? Promise.resolve(list) : api.fleet.networks(slug),
        api.fleet.settings(slug),
      ])
        .then(([n, s]) => {
          if (run !== runId) return;
          networks = n ?? [];
          selection = initialSelection(networks);
          newTracked = s.new_networks_tracked;
          savedNewTracked = s.new_networks_tracked;
        })
        .catch((e) => {
          if (run === runId) error = describeError(e);
        })
        .finally(() => {
          if (run === runId) loading = false;
        });
    });
  });

  async function save() {
    saving = true;
    saved = false;
    error = null;
    try {
      if (networks.length > 0)
        await api.fleet.trackOnly(ws, selectedNetworkIds(networks, selection));
      await api.fleet.setSettings(ws, { new_networks_tracked: newTracked });
      networks = networks.map((n) => ({ ...n, tracked: !!selection[n.id] }));
      saved = true;
      editing = false;
      savedNewTracked = newTracked;
      onsaved?.();
    } catch (e) {
      error = describeError(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="space-y-4">
  {#if showPicker}<p class="text-sm text-muted">
      Untracked networks (for example Wi-Fi, phones or guests) are hidden from the fleet host list,
      counts and alerts. You can change this at any time.
    </p>{/if}
  <FormError id="tracking-error" message={error} />
  {#if loading}
    <ListSkeleton rows={3} cols={2} />
  {:else}
    {#if !showPicker}
      <div class="flex flex-wrap items-center gap-2">
        {#if trackedNets.length === 0}
          <span class="text-sm text-muted">No networks tracked.</span>
        {/if}
        {#each trackedNets as n (n.id)}
          <span
            class="rounded-full border border-border bg-surface-2 px-2.5 py-0.5 text-xs"
            title="{n.host_count} host{n.host_count === 1 ? '' : 's'}">{networkChipLabel(n)}</span
          >
        {/each}
        {#if canManage}
          <button type="button" class={btn} onclick={() => (editing = true)}>Edit</button>
        {/if}
        {#if saved}<span role="status" class="text-sm text-muted">Saved.</span>{/if}
      </div>
    {:else if networks.length === 0}
      <p class="text-sm text-muted">
        No networks discovered yet. Add a connector and run a sync; you can choose later.
      </p>
    {:else}
      <NetworkPicker {networks} bind:selection disabled={!canManage} />
    {/if}
    {#if showPicker}
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={newTracked} disabled={!canManage} />
        Track newly discovered networks by default
      </label>
      {#if canManage}
        <button
          type="button"
          class={btnPrimary}
          disabled={saving || !(dirty || allowUnchanged)}
          onclick={save}
        >
          {saving ? 'Saving...' : saveLabel}
        </button>
        {#if collapsible}<button type="button" class={btn} onclick={cancel}>Cancel</button>{/if}
        {#if saved}<span role="status" class="ml-2 text-sm text-muted">Saved.</span>{/if}
      {:else}
        <p class="text-sm text-muted">You are not allowed to change network tracking.</p>
      {/if}
    {/if}
  {/if}
</div>
