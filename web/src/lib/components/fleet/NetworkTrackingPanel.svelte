<script lang="ts">
  import { api, type FleetNetwork } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { selectedIds, trackedSelection } from '$lib/fleet';
  import { session } from '$lib/session.svelte';
  import { btnPrimary } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    saveLabel = 'Save',
    onsaved,
  }: { ws: string; saveLabel?: string; onsaved?: () => void } = $props();

  let networks = $state<FleetNetwork[]>([]);
  let selection = $state<Record<string, boolean>>({});
  let newTracked = $state(true);
  let loading = $state(true);
  let saving = $state(false);
  let saved = $state(false);
  let error = $state<string | null>(null);

  const canManage = $derived(session.can('fleet.manage'));

  $effect(() => {
    loading = true;
    Promise.all([api.fleet.networks(ws), api.fleet.settings(ws)])
      .then(([n, s]) => {
        networks = n ?? [];
        selection = trackedSelection(networks);
        newTracked = s.new_networks_tracked;
      })
      .catch((e) => (error = describeError(e)))
      .finally(() => (loading = false));
  });

  async function save() {
    saving = true;
    saved = false;
    error = null;
    try {
      if (networks.length > 0) await api.fleet.trackOnly(ws, selectedIds(selection));
      await api.fleet.setSettings(ws, { new_networks_tracked: newTracked });
      saved = true;
      onsaved?.();
    } catch (e) {
      error = describeError(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="space-y-4">
  <p class="text-sm text-muted">
    Untracked networks (for example Wi-Fi, phones or guests) are hidden from the fleet host list,
    counts and alerts. You can change this at any time.
  </p>
  <FormError id="tracking-error" message={error} />
  {#if loading}
    <p class="text-muted">Loading...</p>
  {:else}
    {#if networks.length === 0}
      <p class="text-sm text-muted">
        No networks discovered yet. Add a connector and run a sync; you can choose later.
      </p>
    {:else}
      <fieldset class="space-y-2">
        <legend class="mb-1 text-sm font-medium">Which networks should the fleet track?</legend>
        {#each networks as n (n.id)}
          <label class="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              bind:checked={selection[n.id]}
              disabled={!canManage}
              aria-label="Track {n.cidr}"
            />
            <span class="font-mono">{n.cidr}</span>
            <span class="text-muted">
              {n.description || n.interface || '-'} - {n.host_count} host{n.host_count === 1
                ? ''
                : 's'} via {n.connector}
            </span>
          </label>
        {/each}
      </fieldset>
    {/if}
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={newTracked} disabled={!canManage} />
      Track newly discovered networks by default
    </label>
    {#if canManage}
      <button type="button" class={btnPrimary} disabled={saving} onclick={save}>
        {saving ? 'Saving...' : saveLabel}
      </button>
      {#if saved}<span role="status" class="ml-2 text-sm text-muted">Saved.</span>{/if}
    {:else}
      <p class="text-sm text-muted">You are not allowed to change network tracking.</p>
    {/if}
  {/if}
</div>
