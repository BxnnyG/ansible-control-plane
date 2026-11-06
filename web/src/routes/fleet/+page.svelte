<script lang="ts">
  import { api, type FleetHost, type FleetNetwork } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { card, fmtDate, input, td, th } from '$lib/ui';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let networks = $state<FleetNetwork[]>([]);
  let hosts = $state<FleetHost[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  let network = $state('');
  let q = $state('');
  let conflictsOnly = $state(false);

  const canRead = $derived(session.can('fleet.read'));

  async function loadNetworks(ws: string) {
    try {
      networks = await api.fleet.networks(ws);
    } catch (e) {
      error = describeError(e);
    }
  }

  async function loadHosts(ws: string, f: { network: string; q: string; conflicts: boolean }) {
    loading = true;
    error = null;
    try {
      hosts = await api.fleet.hosts(ws, {
        network: f.network || undefined,
        q: f.q.trim() || undefined,
        conflicts: f.conflicts,
      });
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (session.slug && canRead) void loadNetworks(session.slug);
  });

  // Debounce the search so typing does not fire a request per key.
  $effect(() => {
    const f = { network, q, conflicts: conflictsOnly };
    const ws = session.slug;
    if (!ws || !canRead) {
      loading = false;
      return;
    }
    const t = setTimeout(() => void loadHosts(ws, f), 250);
    return () => clearTimeout(t);
  });
</script>

<svelte:head><title>Fleet - Ansible Control Plane</title></svelte:head>

<header class="mb-4">
  <h1 class="text-2xl font-semibold">Fleet</h1>
  <p class="mt-1 text-muted">Hosts and networks merged from all connectors.</p>
</header>
<FleetTabs />
<FormError id="fleet-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view the fleet in this workspace.</p>
{:else}
  <section aria-labelledby="networks-h" class="mb-8">
    <h2 id="networks-h" class="mb-2 text-lg font-medium">Networks</h2>
    {#if networks.length === 0}
      <p class="text-sm text-muted">No networks discovered yet.</p>
    {:else}
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {#each networks as n (n.id)}
          <button
            type="button"
            class="{card} text-left hover:bg-surface-2 {network === n.id
              ? 'ring-2 ring-accent'
              : ''}"
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
          </button>
        {/each}
      </div>
    {/if}
  </section>

  <section aria-labelledby="hosts-h">
    <h2 id="hosts-h" class="mb-2 text-lg font-medium">Hosts</h2>
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
    </div>
    {#if loading}
      <p class="text-muted">Loading...</p>
    {:else if hosts.length === 0}
      <div class="rounded-lg border border-dashed border-border p-8 text-center text-muted">
        No hosts match.
      </div>
    {:else}
      <div class="overflow-x-auto rounded-lg border border-border">
        <table class="w-full">
          <thead class="bg-surface-2">
            <tr>
              <th class={th}>Name</th><th class={th}>Kind</th><th class={th}>Status</th>
              <th class={th}>IPs</th><th class={th}>Sources</th><th class={th}>Conflicts</th>
              <th class={th}>Last seen</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each hosts as h (h.id)}
              <tr>
                <td class={td}
                  ><a class="text-accent underline" href="/fleet/hosts/{h.id}">{h.name}</a></td
                >
                <td class={td}>{h.kind || '-'}</td>
                <td class={td}>{h.status || '-'}</td>
                <td class="{td} font-mono text-xs">{h.ips?.join(', ') || '-'}</td>
                <td class={td}>{h.connectors?.join(', ') || '-'}</td>
                <td class={td}>
                  {#if h.open_conflicts > 0}
                    <span class="text-red-600 dark:text-red-400">{h.open_conflicts}</span>
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
