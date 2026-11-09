<script lang="ts">
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { page } from '$app/state';
  import { api, type FleetAlert, type FleetHostDetail, type RemediationRef } from '$lib/api';
  import { describeError } from '$lib/errors';
  import {
    alertBadge,
    ansibleClass,
    ansibleLabel,
    conflictDetails,
    conflictLabel,
    driftLabel,
    fieldRows,
    onlineClass,
    onlineLabel,
    alertHost,
    severityClass,
    severityLabel,
  } from '$lib/fleet';
  import { rulesFor } from '$lib/remediation';
  import FixButton from '$lib/components/fleet/FixButton.svelte';
  import { session } from '$lib/session.svelte';
  import { fmtDate, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let host = $state<FleetHostDetail | null>(null);
  let error = $state<string | null>(null);
  let loading = $state(true);
  let alerts = $state<FleetAlert[]>([]);
  let applicable = $state<Record<string, RemediationRef[]>>({});

  const canRead = $derived(session.can('fleet.read'));
  const canFix = $derived(session.can('fleet.remediate'));
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
      .then(async (h) => {
        host = h;
        alerts = [];
        applicable = {};
        if (h.open_alerts > 0) {
          // Alerts and fix suggestions are extras: failing here keeps the page usable.
          alerts = await api.fleet
            .alerts(ws, { state: 'open', host_id: h.id, includeUntracked: true })
            .catch(() => []);
          if (canFix) applicable = await api.remediation.applicable(ws).catch(() => ({}));
        }
      })
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
  <ListSkeleton />
{:else if host}
  <header class="mb-6">
    <h1 class="text-2xl font-semibold sm:text-3xl">{host.name}</h1>
    <p class="mt-1 text-muted">
      First seen {fmtDate(host.first_seen)}, last seen {fmtDate(host.last_seen)}
    </p>
    {#if host.open_alerts > 0}
      <p class="mt-2 text-sm">
        <a
          href="/fleet/alerts?host_id={host.id}"
          class="rounded px-2 py-0.5 {severityClass(host.worst_severity)}"
          >Open alerts: {alertBadge(host)}</a
        >
      </p>
    {/if}
  </header>

  <section aria-labelledby="status-h" class="mb-8">
    <h2 id="status-h" class="mb-2 text-lg font-medium">Status</h2>
    <dl class="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[max-content_1fr]">
      <dt class="text-muted">Online</dt>
      <dd>
        <span class="rounded px-2 py-0.5 text-xs {onlineClass(host.online)}"
          >{onlineLabel(host.online)}</span
        >
      </dd>
      <dt class="text-muted">Ansible</dt>
      <dd>
        <span class="rounded px-2 py-0.5 text-xs {ansibleClass(host)}">{ansibleLabel(host)}</span>
        {#each host.ansible as l (l.host_id)}
          <a class="ml-2 text-accent underline" href="/inventories/{l.inventory_id}"
            >{l.host} in {l.inventory}</a
          >
        {/each}
      </dd>
      {#if host.last_run}
        <dt class="text-muted">Last run</dt>
        <dd>
          <a class="text-accent underline" href="/runs/{host.last_run.run_id}"
            >{host.last_run.template}</a
          >
          - {host.last_run.result}{host.last_run.check ? ' (check)' : ''},
          {fmtDate(host.last_run.finished_at)}
        </dd>
      {/if}
      <dt class="text-muted">Reachable</dt>
      <dd>{host.reachable}</dd>
      {#if host.drift}
        <dt class="text-muted">Drift</dt>
        <dd>
          <a class="text-accent underline" href="/runs/{host.drift.run_id}">{driftLabel(host)}</a>
          in check run of {host.drift.template}, {fmtDate(host.drift.finished_at)}
        </dd>
      {/if}
    </dl>
  </section>

  {#if alerts.length > 0}
    <section aria-labelledby="alerts-h" class="mb-8">
      <h2 id="alerts-h" class="mb-2 text-lg font-medium">Open alerts</h2>
      <ul class="space-y-2">
        {#each alerts as a (a.id)}
          <li class="rounded-lg border border-border p-3 text-sm">
            <span class="rounded px-2 py-0.5 text-xs {severityClass(a.severity)}"
              >{severityLabel(a.severity)}</span
            >
            {a.service || '-'} - {a.summary || alertHost(a)}
            {#if canFix && session.slug}
              <FixButton ws={session.slug} alertId={a.id} rules={rulesFor(applicable, a.id)} />
            {/if}
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  <section aria-labelledby="fields-h" class="mb-8">
    <h2 id="fields-h" class="mb-2 text-lg font-medium">Merged fields</h2>
    <div class="tbl-wrap">
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
    <div class="tbl-wrap">
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
