<script lang="ts">
  import { api, type FleetConflict } from '$lib/api';
  import { describeError } from '$lib/errors';
  import {
    canAck,
    conflictDetails,
    conflictHosts,
    conflictLabel,
    stateParam,
    type ConflictFilter,
  } from '$lib/fleet';
  import { session } from '$lib/session.svelte';
  import { btn, fmtDate, input, td, th } from '$lib/ui';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let items = $state<FleetConflict[]>([]);
  let filter = $state<ConflictFilter>('open');
  let loading = $state(true);
  let error = $state<string | null>(null);
  let busyId = $state<string | null>(null);

  const canRead = $derived(session.can('fleet.read'));
  const canManage = $derived(session.can('fleet.manage'));

  async function load(ws: string, f: ConflictFilter) {
    loading = true;
    error = null;
    try {
      items = await api.fleet.conflicts(ws, stateParam(f));
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (session.slug && canRead) void load(session.slug, filter);
    else loading = false;
  });

  async function ack(c: FleetConflict) {
    if (!session.slug) return;
    busyId = c.id;
    try {
      await api.fleet.ackConflict(session.slug, c.id);
      await load(session.slug, filter);
    } catch (e) {
      error = describeError(e);
    } finally {
      busyId = null;
    }
  }
</script>

<svelte:head><title>Conflicts - Ansible Control Plane</title></svelte:head>

<header class="mb-4">
  <h1 class="text-2xl font-semibold">Conflicts</h1>
  <p class="mt-1 text-muted">Inconsistencies between connectors, such as duplicate addresses.</p>
</header>
<FleetTabs />
<FormError id="conflicts-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view the fleet in this workspace.</p>
{:else}
  <div class="mb-3">
    <select aria-label="State" class="{input} max-w-xs" bind:value={filter}>
      <option value="open">Open</option>
      <option value="acknowledged">Acknowledged</option>
      <option value="resolved">Resolved</option>
      <option value="all">All</option>
    </select>
  </div>
  {#if loading}
    <p class="text-muted">Loading...</p>
  {:else if items.length === 0}
    <div class="rounded-lg border border-dashed border-border p-8 text-center text-muted">
      No conflicts.
    </div>
  {:else}
    <div class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Type</th><th class={th}>Details</th><th class={th}>Hosts</th>
            <th class={th}>State</th><th class={th}>Last seen</th>
            <th class={th}><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as c (c.id)}
            <tr>
              <td class={td}>{conflictLabel(c.type)}</td>
              <td class="{td} font-mono text-xs break-all">
                {#each conflictDetails(c) as line (line)}<div>{line}</div>{/each}
              </td>
              <td class={td}>
                {#each conflictHosts(c) as h (h.id)}
                  <a class="mr-2 text-accent underline" href="/fleet/hosts/{h.id}">{h.label}</a>
                {/each}
              </td>
              <td class={td}>
                {c.state}
                {#if c.acknowledged_at}
                  <div class="text-xs text-muted">{fmtDate(c.acknowledged_at)}</div>
                {/if}
              </td>
              <td class={td}>{fmtDate(c.last_seen)}</td>
              <td class="{td} text-right">
                {#if canManage && canAck(c)}
                  <button class={btn} disabled={busyId === c.id} onclick={() => ack(c)}>
                    Acknowledge
                  </button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
