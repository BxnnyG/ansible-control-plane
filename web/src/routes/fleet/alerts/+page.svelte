<script lang="ts">
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { page } from '$app/state';
  import { api, type FleetAlert, type RemediationRef } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { alertHost, alertLabels, severityClass, severityLabel } from '$lib/fleet';
  import { session } from '$lib/session.svelte';
  import { fmtDate, input, td, th } from '$lib/ui';
  import { rulesFor } from '$lib/remediation';
  import FixButton from '$lib/components/fleet/FixButton.svelte';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import FormError from '$lib/components/FormError.svelte';

  type StateFilter = 'open' | 'resolved' | 'all';

  let items = $state<FleetAlert[]>([]);
  let applicable = $state<Record<string, RemediationRef[]>>({});
  let filter = $state<StateFilter>('open');
  let loading = $state(true);
  let error = $state<string | null>(null);

  const canRead = $derived(session.can('fleet.read'));
  const canFix = $derived(session.can('fleet.remediate'));
  // Linked from the host list: /fleet/alerts?host_id=<id>
  const hostId = $derived(page.url.searchParams.get('host_id') ?? '');

  async function load(ws: string, f: StateFilter, host: string) {
    loading = true;
    error = null;
    try {
      items = await api.fleet.alerts(ws, {
        state: f === 'all' ? undefined : f,
        host_id: host || undefined,
      });
      // Missing fix suggestions must never hide the alerts themselves.
      applicable = canFix ? await api.remediation.applicable(ws).catch(() => ({})) : {};
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (session.slug && canRead) void load(session.slug, filter, hostId);
    else loading = false;
  });
</script>

<svelte:head><title>Alerts - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Alerts"
  description="Monitoring alerts from Checkmk and alert webhooks, linked to hosts."
/>
<FleetTabs />
<FormError id="alerts-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view the fleet in this workspace.</p>
{:else}
  <div class="mb-3 flex flex-wrap items-center gap-3">
    <select aria-label="State" class="{input} max-w-xs" bind:value={filter}>
      <option value="open">Open</option>
      <option value="resolved">Resolved</option>
      <option value="all">All</option>
    </select>
    {#if hostId}
      <span class="text-sm text-muted">
        Filtered by host. <a class="text-accent underline" href="/fleet/alerts">Show all</a>
      </span>
    {/if}
  </div>
  {#if loading}
    <ListSkeleton />
  {:else if items.length === 0}
    <div class="empty">No alerts.</div>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Severity</th><th class={th}>Host</th><th class={th}>Service</th>
            <th class={th}>Summary</th><th class={th}>Source</th><th class={th}>Since</th>
            <th class={th}>State</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as a (a.id)}
            <tr>
              <td class={td}>
                <span class="rounded px-2 py-0.5 text-xs {severityClass(a.severity)}">
                  {severityLabel(a.severity)}
                </span>
              </td>
              <td class={td}>
                {#if a.host_id}
                  <a class="text-accent underline" href="/fleet/hosts/{a.host_id}">{alertHost(a)}</a
                  >
                {:else}
                  {alertHost(a)} <span class="text-xs text-muted">(unlinked)</span>
                {/if}
              </td>
              <td class={td}>{a.service || '-'}</td>
              <td class="{td} max-w-md text-sm break-words">
                {a.summary || '-'}
                {#each alertLabels(a) as l (l)}
                  <div class="font-mono text-xs text-muted">{l}</div>
                {/each}
                {#if canFix && !a.resolved_at && session.slug}
                  <FixButton ws={session.slug} alertId={a.id} rules={rulesFor(applicable, a.id)} />
                {/if}
              </td>
              <td class={td}>{a.connector}</td>
              <td class={td}>{fmtDate(a.started_at)}</td>
              <td class={td}>
                {a.resolved_at ? `Resolved ${fmtDate(a.resolved_at)}` : 'Open'}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
