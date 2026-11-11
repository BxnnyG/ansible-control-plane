<script lang="ts">
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { api, type FleetHost, type FleetNetwork, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { page } from '$app/state';
  import { session } from '$lib/session.svelte';
  import { fleetFiltersFromParams, fleetFiltersToUpdates } from '$lib/urlState';
  import { syncUrl } from '$lib/urlSync';
  import {
    alertBadge,
    ansibleClass,
    ansibleLabel,
    driftLabel,
    hiddenNetworkCount,
    onlineClass,
    onlineLabel,
    severityClass,
  } from '$lib/fleet';
  import { btnPrimary, card, fmtDate, input, td, th } from '$lib/ui';
  import BulkRunDialog from '$lib/components/fleet/BulkRunDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import AddConnectorLink from '$lib/components/fleet/AddConnectorLink.svelte';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import NetworkTrackingPanel from '$lib/components/fleet/NetworkTrackingPanel.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let networks = $state<FleetNetwork[]>([]);
  let hosts = $state<FleetHost[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  // Filters live in the URL so a reload or shared link keeps them.
  const initial = untrack(() => fleetFiltersFromParams(page.url.searchParams));
  let network = $state(initial.network);
  let q = $state(initial.q);
  let qDebounced = $state(initial.q);
  let conflictsOnly = $state(initial.conflicts);
  let alertsOnly = $state(initial.alerts);
  let showHidden = $state(initial.hidden);
  let networksOpen = $state(false);

  const trackedCount = $derived(networks.filter((n) => n.tracked).length);
  const hidden = $derived(hiddenNetworkCount(networks));

  const canRead = $derived(session.can('fleet.read'));
  const canBulk = $derived(session.can('run.execute'));

  let selected = $state<Record<string, boolean>>({});
  let templates = $state<Template[]>([]);
  let bulkOpen = $state(false);
  const chosen = $derived(hosts.filter((h) => selected[h.id]));
  const allChosen = $derived(hosts.length > 0 && chosen.length === hosts.length);

  function toggleAll(on: boolean) {
    selected = on ? Object.fromEntries(hosts.map((h) => [h.id, true])) : {};
  }

  async function openBulk() {
    if (!session.slug) return;
    error = null;
    try {
      templates = await api.templates.list(session.slug);
      bulkOpen = true;
    } catch (e) {
      error = describeError(e);
    }
  }

  async function loadNetworks(ws: string) {
    try {
      networks = await api.fleet.networks(ws);
    } catch (e) {
      error = describeError(e);
    }
  }

  /** After the tracking panel saved: refresh networks and the host list. */
  async function onTrackingSaved() {
    const ws = session.slug;
    if (!ws) return;
    await loadNetworks(ws);
    if (network && !networks.find((n) => n.id === network)?.tracked && !showHidden) network = '';
    await loadHosts(ws, {
      network,
      q: qDebounced,
      conflicts: conflictsOnly,
      alerts: alertsOnly,
      showHidden,
    });
  }

  const filtersActive = $derived(!!(network || qDebounced.trim() || conflictsOnly || alertsOnly));
  let hostSeq = 0;
  async function loadHosts(
    ws: string,
    f: {
      network: string;
      q: string;
      conflicts: boolean;
      alerts: boolean;
      showHidden: boolean;
    },
  ) {
    loading = true;
    error = null;
    const seq = ++hostSeq;
    try {
      const list = await api.fleet.hosts(ws, {
        network: f.network || undefined,
        q: f.q.trim() || undefined,
        conflicts: f.conflicts,
        alerts: f.alerts,
        includeUntracked: f.showHidden,
      });
      if (seq !== hostSeq) return; // a newer request superseded this one
      hosts = list;
      // Keep the selection to hosts still listed.
      selected = Object.fromEntries(hosts.filter((h) => selected[h.id]).map((h) => [h.id, true]));
    } catch (e) {
      if (seq === hostSeq) error = describeError(e);
    } finally {
      if (seq === hostSeq) loading = false;
    }
  }

  $effect(() => {
    const ws = session.slug;
    if (ws && canRead) untrack(() => void loadNetworks(ws));
  });

  // Only the search text is debounced; every other filter loads immediately.
  $effect(() => {
    const v = q;
    if (v === untrack(() => qDebounced)) return;
    const t = setTimeout(() => (qDebounced = v), 250);
    return () => clearTimeout(t);
  });

  $effect(() => {
    const u = fleetFiltersToUpdates({
      network,
      q: qDebounced,
      conflicts: conflictsOnly,
      alerts: alertsOnly,
      hidden: showHidden,
    });
    untrack(() => syncUrl(u));
  });

  $effect(() => {
    const f = { network, q: qDebounced, conflicts: conflictsOnly, alerts: alertsOnly, showHidden };
    const ws = session.slug;
    if (!ws || !canRead) {
      loading = false;
      return;
    }
    untrack(() => void loadHosts(ws, f));
  });
</script>

<svelte:head><title>Hosts - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Hosts"
  description="Every machine your connectors have seen, merged into one list, with online, Ansible and alert status."
/>
<FleetTabs />
<FormError id="fleet-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view hosts in this workspace.</p>
{:else}
  <section aria-labelledby="networks-h" class="mb-6">
    <details
      class="{card} !p-0"
      ontoggle={(e) => (networksOpen = (e.currentTarget as HTMLDetailsElement).open)}
    >
      <summary class="flex cursor-pointer flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3">
        <h2 id="networks-h" class="text-sm font-semibold">Networks</h2>
        <span class="text-sm text-muted">
          {trackedCount} of {networks.length} tracked{hidden > 0 ? `, ${hidden} hidden` : ''}
        </span>
      </summary>
      <div class="border-t border-border px-4 py-4">
        {#if networksOpen && session.slug}
          <NetworkTrackingPanel
            ws={session.slug}
            {networks}
            saveLabel="Save tracking"
            onsaved={onTrackingSaved}
          />
        {/if}
      </div>
    </details>
    {#if networks.length > 0}
      <label class="mt-2 flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={showHidden} /> Show hosts of untracked networks
      </label>
    {/if}
  </section>

  <section aria-labelledby="hosts-h">
    <h2 id="hosts-h" class="mb-2 text-sm font-semibold">Hosts</h2>
    <div class="mb-3 flex flex-wrap items-center gap-3">
      <input
        type="search"
        aria-label="Search hosts"
        placeholder="Search name, IP, MAC"
        class="{input} max-w-xs"
        bind:value={q}
      />
      <select aria-label="Network" class="{input} max-w-xs" bind:value={network}>
        <option value="">All networks</option>
        {#each networks as n (n.id)}<option value={n.id}>{n.cidr}</option>{/each}
      </select>
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={conflictsOnly} /> Has conflicts
      </label>
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={alertsOnly} /> Has alerts
      </label>
      {#if canBulk && chosen.length > 0}
        <button class={btnPrimary} onclick={openBulk}
          >Run template on {chosen.length} host(s)</button
        >
      {/if}
    </div>
    {#if bulkOpen && session.slug}
      <BulkRunDialog
        ws={session.slug}
        hosts={chosen}
        {templates}
        onclose={() => (bulkOpen = false)}
      />
    {/if}
    {#if loading}
      <ListSkeleton />
    {:else if hosts.length === 0}
      {#if filtersActive}
        <div class="empty">No hosts match the filters.</div>
      {:else}
        <EmptyState
          title="No hosts yet"
          hint="Hosts appear after a connector (OPNsense, Proxmox, NetBox, Checkmk) has synced."
        >
          <AddConnectorLink />
        </EmptyState>
      {/if}
    {:else}
      <div class="tbl-wrap">
        <table class="w-full">
          <thead class="bg-surface-2">
            <tr>
              {#if canBulk}
                <th class={th}>
                  <input
                    type="checkbox"
                    aria-label="Select all hosts"
                    checked={allChosen}
                    onchange={(e) => toggleAll(e.currentTarget.checked)}
                  />
                </th>
              {/if}
              <th class={th}>Name</th><th class={th}>Kind</th><th class={th}>Online</th>
              <th class={th}>Ansible</th><th class={th}>Drift</th>
              <th class={th}>IPs</th><th class={th}>Sources</th><th class={th}>Alerts</th>
              <th class={th}>Conflicts</th>
              <th class={th}>Last seen</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each hosts as h (h.id)}
              <tr>
                {#if canBulk}
                  <td class={td}>
                    <input
                      type="checkbox"
                      aria-label="Select {h.name}"
                      bind:checked={selected[h.id]}
                    />
                  </td>
                {/if}
                <td class={td}
                  ><a
                    class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                    href="/fleet/hosts/{h.id}">{h.name}</a
                  ></td
                >
                <td class={td}>{h.kind || '-'}</td>
                <td class={td}>
                  <span class="rounded px-2 py-0.5 text-xs {onlineClass(h.online)}"
                    >{onlineLabel(h.online)}</span
                  >
                </td>
                <td class={td}>
                  <span class="rounded px-2 py-0.5 text-xs {ansibleClass(h)}"
                    >{ansibleLabel(h)}</span
                  >
                  {#if h.reachable === 'no'}<span class="ml-1 text-xs text-bad">unreachable</span
                    >{/if}
                </td>
                <td class={td}>
                  {#if h.drift}
                    <a class="tone-warn" href="/runs/{h.drift.run_id}">{driftLabel(h)}</a>
                  {:else}-{/if}
                </td>
                <td class="{td} font-mono text-xs">{h.ips?.join(', ') || '-'}</td>
                <td class={td}>{h.connectors?.join(', ') || '-'}</td>
                <td class={td}>
                  {#if h.open_alerts > 0}
                    <a
                      href="/fleet/alerts?host_id={h.id}"
                      class="rounded px-2 py-0.5 text-xs {severityClass(h.worst_severity)}"
                      >{alertBadge(h)}</a
                    >
                  {:else}-{/if}
                </td>
                <td class={td}>
                  {#if h.open_conflicts > 0}
                    <span class="text-bad">{h.open_conflicts}</span>
                  {:else}0{/if}
                </td>
                <td class={td}>{fmtDate(h.last_seen)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
{/if}
