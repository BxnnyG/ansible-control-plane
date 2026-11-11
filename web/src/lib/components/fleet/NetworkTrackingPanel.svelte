<script lang="ts">
  import { untrack } from 'svelte';
  import { api, type FleetNetwork } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { initialSelection, isDirty, selectedNetworkIds } from '$lib/networkPicker';
  import { session } from '$lib/session.svelte';
  import { btnPrimary } from '$lib/ui';
  import NetworkPicker from '$lib/components/fleet/NetworkPicker.svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    networks: given,
    saveLabel = 'Save',
    allowUnchanged = false,
    onsaved,
  }: {
    ws: string;
    /** Already loaded networks; the panel fetches them itself when omitted. */
    networks?: FleetNetwork[];
    saveLabel?: string;
    /** Keeps Save enabled without changes (setup wizard: Save continues). */
    allowUnchanged?: boolean;
    onsaved?: () => void;
  } = $props();

  let networks = $state<FleetNetwork[]>([]);
  let selection = $state<Record<string, boolean>>({});
  let newTracked = $state(true);
  let savedNewTracked = $state(true);
  let loading = $state(true);
  let saving = $state(false);
  let saved = $state(false);
  let error = $state<string | null>(null);

  const canManage = $derived(session.can('fleet.manage'));
  const dirty = $derived(isDirty(networks, selection) || newTracked !== savedNewTracked);

  $effect(() => {
    const slug = ws;
    const list = given;
    untrack(() => {
      loading = true;
      Promise.all([
        list ? Promise.resolve(list) : api.fleet.networks(slug),
        api.fleet.settings(slug),
      ])
        .then(([n, s]) => {
          networks = n ?? [];
          selection = initialSelection(networks);
          newTracked = s.new_networks_tracked;
          savedNewTracked = s.new_networks_tracked;
        })
        .catch((e) => (error = describeError(e)))
        .finally(() => (loading = false));
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
  <p class="text-sm text-muted">
    Untracked networks (for example Wi-Fi, phones or guests) are hidden from the fleet host list,
    counts and alerts. You can change this at any time.
  </p>
  <FormError id="tracking-error" message={error} />
  {#if loading}
    <ListSkeleton rows={3} cols={2} />
  {:else}
    {#if networks.length === 0}
      <p class="text-sm text-muted">
        No networks discovered yet. Add a connector and run a sync; you can choose later.
      </p>
    {:else}
      <NetworkPicker {networks} bind:selection disabled={!canManage} />
    {/if}
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
      {#if saved}<span role="status" class="ml-2 text-sm text-muted">Saved.</span>{/if}
    {:else}
      <p class="text-sm text-muted">You are not allowed to change network tracking.</p>
    {/if}
  {/if}
</div>
