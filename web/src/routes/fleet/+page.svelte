<script lang="ts">
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { api, type FleetHost, type FleetNetwork, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import {
    alertBadge,
    ansibleClass,
    ansibleLabel,
    driftLabel,
    hiddenNetworkCount,
    listedNetworks,
    selectedIds,
    networkCountsLabel,
    onlineClass,
    onlineLabel,
    severityClass,
  } from '$lib/fleet';
  import { btn, btnPrimary, card, fmtDate, input, td, th } from '$lib/ui';
  import BulkRunDialog from '$lib/components/fleet/BulkRunDialog.svelte';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let networks = $state<FleetNetwork[]>([]);
  let hosts = $state<FleetHost[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  let network = $state('');
  let q = $state('');
  let conflictsOnly = $state(false);
  let alertsOnly = $state(false);
  let showHidden = $state(false);
  let pick = $state(false); // "track only selected" mode
  let picked = $state<Record<string, boolean>>({});
  let trackBusy = $state(false);

  const canManage = $derived(session.can('fleet.manage'));
  const shownNetworks = $derived(listedNetworks(networks, showHidden));
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

  async function setTracked(n: FleetNetwork, tracked: boolean) {
    if (!session.slug || trackBusy) return;
    trackBusy = true;
    error = null;
    try {
      await api.fleet.trackNetwork(session.slug, n.id, tracked);
      n.tracked = tracked;
      if (!tracked && !showHidden && network === n.id) network = '';
      void loadHosts(session.slug, {
        network,
        q,
        conflicts: conflictsOnly,
        alerts: alertsOnly,
        showHidden,
      });
    } catch (e) {
      error = describeError(e);
    } finally {
      trackBusy = false;
    }
  }

  function startPick() {
    picked = Object.fromEntries(networks.filter((n) => n.tracked).map((n) => [n.id, true]));
    pick = true;
  }

  async function applyPick() {
    if (!session.slug || trackBusy) return;
    trackBusy = true;
    error = null;
    try {
      await api.fleet.trackOnly(session.slug, selectedIds(picked));
      pick = false;
      await loadNetworks(session.slug);
      if (network && !networks.find((n) => n.id === network)?.tracked && !showHidden) network = '';
      await loadHosts(session.slug, {
        network,
        q,
        conflicts: conflictsOnly,
        alerts: alertsOnly,
        showHidden,
      });
    } catch (e) {
      error = describeError(e);
    } finally {
      trackBusy = false;
    }
  }

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
  let qDebounced = $state('');
  $effect(() => {
    const v = q;
    if (v === untrack(() => qDebounced)) return;
    const t = setTimeout(() => (qDebounced = v), 250);
    return () => clearTimeout(t);
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

<svelte:head><title>Fleet - Ansible Control Plane</title></svelte:head>

<PageHeader title="Fleet" description="Hosts and networks merged from all connectors." />
<FleetTabs />
<FormError id="fleet-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view the fleet in this workspace.</p>
{:else}
  <section aria-labelledby="networks-h" class="mb-8">
    <div class="mb-2 flex flex-wrap items-center gap-3">
      <h2 id="networks-h" class="text-sm font-semibold">Networks</h2>
      {#if networks.length > 0}
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={showHidden} /> Show hidden networks{hidden > 0
            ? ` (${hidden})`
            : ''}
        </label>
        {#if canManage && !pick}
          <button type="button" class={btn} onclick={startPick}>Track only selected...</button>
        {/if}
        {#if pick}
          <button type="button" class={btnPrimary} disabled={trackBusy} onclick={applyPick}
            >Track only the {selectedIds(picked).length} selected</button
          >
          <button type="button" class={btn} onclick={() => (pick = false)}>Cancel</button>
        {/if}
      {/if}
    </div>
    {#if networks.length === 0}
      <p class="text-sm text-muted">No networks discovered yet.</p>
    {:else if shownNetworks.length === 0}
      <p class="text-sm text-muted">All networks are hidden. Turn on "Show hidden networks".</p>
    {:else}
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {#each shownNetworks as n (n.id)}
          <div
            class="{card} {n.tracked ? '' : 'opacity-60'} {network === n.id
              ? 'ring-2 ring-accent'
              : ''}"
          >
            <button
              type="button"
              class="block w-full text-left"
              aria-pressed={network === n.id}
              onclick={() => (network = network === n.id ? '' : n.id)}
            >
              <div class="font-mono text-sm font-medium">{n.cidr}</div>
              <div class="text-xs text-muted">
                {n.description || n.interface || '-'}{n.vlan ? ` - VLAN ${n.vlan}` : ''}
              </div>
              <div class="mt-2 text-sm">
                {n.host_count} host{n.host_count === 1 ? '' : 's'}
                <span class="text-xs text-muted">via {n.connector}</span>
              </div>
              <div class="text-xs text-muted">
                {networkCountsLabel(n.counts)}{n.counts?.open_alerts
                  ? `, ${n.counts.open_alerts} alert${n.counts.open_alerts === 1 ? '' : 's'}`
                  : ''}
              </div>
            </button>
            {#if pick}
              <label class="mt-2 flex items-center gap-2 text-sm">
                <input type="checkbox" bind:checked={picked[n.id]} aria-label="Select {n.cidr}" />
                Select
              </label>
            {:else if canManage}
              <label class="mt-2 flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={n.tracked}
                  disabled={trackBusy}
                  aria-label="Tracked {n.cidr}"
                  onchange={(e) => setTracked(n, e.currentTarget.checked)}
                />
                Tracked
              </label>
            {:else if !n.tracked}
              <p class="mt-2 text-xs text-muted">Not tracked</p>
            {/if}
          </div>
        {/each}
      </div>
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
      <div class="empty">No hosts match.</div>
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
