<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { enumParam } from '$lib/urlState';
  import { syncUrl } from '$lib/urlSync';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
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
  import EmptyState from '$lib/components/EmptyState.svelte';
  import AddConnectorLink from '$lib/components/fleet/AddConnectorLink.svelte';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let items = $state<FleetConflict[]>([]);
  const STATES = ['open', 'acknowledged', 'resolved', 'all'] as const;
  let filter = $state<ConflictFilter>(
    untrack(() => enumParam(page.url.searchParams, 'state', STATES, 'open')),
  );
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
    const f = filter;
    untrack(() => syncUrl({ state: f === 'open' ? '' : f }));
  });

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

<svelte:head><title>Conflicts - Hosts - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Conflicts"
  description="Inconsistencies between connectors, such as duplicate addresses."
/>
<FleetTabs />
<FormError id="conflicts-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view hosts in this workspace.</p>
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
    <ListSkeleton />
  {:else if items.length === 0}
    <EmptyState
      title="No conflicts"
      hint="Conflicts show where connectors disagree about a host, for example duplicate addresses."
    >
      <AddConnectorLink />
    </EmptyState>
  {:else}
    <div class="tbl-wrap">
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
                  <a
                    class="mr-2 underline decoration-border-strong underline-offset-2 hover:decoration-current"
                    href="/fleet/hosts/{h.id}">{h.label}</a
                  >
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
