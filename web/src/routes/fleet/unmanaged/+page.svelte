<script lang="ts">
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { api, type FleetHost, type Inventory } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { alertBadge, onlineClass, onlineLabel, severityClass } from '$lib/fleet';
  import { btn, fmtDate, input, td, th } from '$lib/ui';
  import AddToInventoryDialog from '$lib/components/fleet/AddToInventoryDialog.svelte';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let hosts = $state<FleetHost[]>([]);
  let inventories = $state<Inventory[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let notice = $state<string | null>(null);
  let q = $state('');
  let adding = $state<FleetHost | null>(null);
  let reload = $state(0);

  const canRead = $derived(session.can('fleet.read'));
  const canAdd = $derived(session.can('inventory.write'));

  async function load(ws: string, query: string) {
    loading = true;
    error = null;
    try {
      hosts = await api.fleet.hosts(ws, { managed: false, q: query.trim() || undefined });
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    const ws = session.slug;
    void reload;
    if (!ws || !canRead) {
      loading = false;
      return;
    }
    const t = setTimeout(() => void load(ws, q), 250);
    return () => clearTimeout(t);
  });

  $effect(() => {
    const ws = session.slug;
    if (!ws || !canAdd) return;
    api.inventories
      .list(ws)
      .then((l) => (inventories = l))
      .catch((e) => (error = describeError(e)));
  });
</script>

<svelte:head><title>Unmanaged hosts - Ansible Control Plane</title></svelte:head>

<PageHeader title="Fleet" description="Hosts that no inventory of this workspace manages yet." />
<FleetTabs />
<FormError id="unmanaged-error" message={error} />
{#if notice}<p role="status" class="mb-3 text-sm text-ok">
    {notice}
  </p>{/if}

{#if !canRead}
  <p class="text-muted">You do not have permission to view the fleet in this workspace.</p>
{:else}
  <div class="mb-3">
    <input
      type="search"
      aria-label="Search hosts"
      placeholder="Search name, IP, MAC"
      class="{input} max-w-xs"
      bind:value={q}
    />
  </div>
  {#if loading}
    <ListSkeleton />
  {:else if hosts.length === 0}
    <div class="empty">Every host is linked to an inventory host.</div>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Kind</th><th class={th}>Online</th>
            <th class={th}>IPs</th><th class={th}>Sources</th><th class={th}>Alerts</th>
            <th class={th}>Last seen</th><th class={th}><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each hosts as h (h.id)}
            <tr>
              <td class={td}
                ><a
                  class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                  href="/fleet/hosts/{h.id}">{h.name || h.id}</a
                ></td
              >
              <td class={td}>{h.kind || '-'}</td>
              <td class={td}
                ><span class="rounded px-2 py-0.5 text-xs {onlineClass(h.online)}"
                  >{onlineLabel(h.online)}</span
                ></td
              >
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
              <td class={td}>{fmtDate(h.last_seen)}</td>
              <td class={td}>
                {#if canAdd}
                  <button type="button" class={btn} onclick={() => (adding = h)}
                    >Add to inventory</button
                  >
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}

{#if adding && session.slug}
  <AddToInventoryDialog
    ws={session.slug}
    host={adding}
    {inventories}
    onclose={() => (adding = null)}
    ondone={(m) => {
      notice = m;
      adding = null;
      reload++;
    }}
  />
{/if}
