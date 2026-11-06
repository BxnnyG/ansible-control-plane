<script lang="ts">
  import { page } from '$app/state';
  import { api, type FleetHostDetail } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { conflictDetails, conflictLabel, fieldRows } from '$lib/fleet';
  import { session } from '$lib/session.svelte';
  import { fmtDate, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let host = $state<FleetHostDetail | null>(null);
  let error = $state<string | null>(null);
  let loading = $state(true);

  const canRead = $derived(session.can('fleet.read'));
  const rows = $derived(host ? fieldRows(host) : []);

  $effect(() => {
    const ws = session.slug;
    const id = page.params.id;
    if (!ws || !id || !canRead) {
      loading = false;
      return;
    }
    loading = true;
    error = null;
    api.fleet
      .host(ws, id)
      .then((h) => (host = h))
      .catch((e) => (error = describeError(e)))
      .finally(() => (loading = false));
  });
</script>

<svelte:head><title>{host?.name ?? 'Host'} - Ansible Control Plane</title></svelte:head>

<p class="mb-2 text-sm"><a class="text-accent underline" href="/fleet">Back to fleet</a></p>
<FormError id="host-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view the fleet in this workspace.</p>
{:else if loading}
  <p class="text-muted">Loading...</p>
{:else if host}
  <header class="mb-6">
    <h1 class="text-2xl font-semibold">{host.name}</h1>
    <p class="mt-1 text-muted">
      First seen {fmtDate(host.first_seen)}, last seen {fmtDate(host.last_seen)}
    </p>
  </header>

  <section aria-labelledby="fields-h" class="mb-8">
    <h2 id="fields-h" class="mb-2 text-lg font-medium">Merged fields</h2>
    <div class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr><th class={th}>Field</th><th class={th}>Value</th><th class={th}>Source</th></tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each rows as r (r.key)}
            <tr>
              <td class={td}>{r.label}</td>
              <td class="{td} font-mono text-xs">
                {#each r.values as v (v)}<div>{v}</div>{/each}
              </td>
              <td class={td}>{r.sources.join(', ') || '-'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  {#if host.conflicts?.length}
    <section aria-labelledby="hc-h" class="mb-8">
      <h2 id="hc-h" class="mb-2 text-lg font-medium">Conflicts</h2>
      <ul class="space-y-2">
        {#each host.conflicts as c (c.id)}
          <li class="rounded-lg border border-border p-3 text-sm">
            <strong>{conflictLabel(c.type)}</strong> ({c.state})
            {#each conflictDetails(c) as line (line)}
              <div class="font-mono text-xs text-muted">{line}</div>
            {/each}
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  <section aria-labelledby="obs-h">
    <h2 id="obs-h" class="mb-2 text-lg font-medium">Observations</h2>
    <div class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Connector</th><th class={th}>External key</th><th class={th}>Hostname</th
            >
            <th class={th}>IPs</th><th class={th}>MACs</th><th class={th}>Tags</th>
            <th class={th}>Last seen</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each host.observations as o (o.connector_id + o.external_key)}
            <tr>
              <td class={td}
                >{o.connector} <span class="text-xs text-muted">({o.connector_type})</span></td
              >
              <td class="{td} font-mono text-xs break-all">{o.external_key}</td>
              <td class={td}>{o.fields?.hostname || o.fields?.fqdn || '-'}</td>
              <td class="{td} font-mono text-xs">{o.fields?.ips?.join(', ') || '-'}</td>
              <td class="{td} font-mono text-xs">{o.fields?.macs?.join(', ') || '-'}</td>
              <td class={td}>{o.fields?.tags?.join(', ') || '-'}</td>
              <td class={td}>{fmtDate(o.last_seen)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>
{/if}
