<script lang="ts">
  import { api, type Connector, type FleetAlert, type FleetHost, type Run } from '$lib/api';
  import {
    connectorHealth,
    failingTemplates,
    fleetStats,
    recentOutcomes,
    relativeTime,
    runStats,
  } from '$lib/dashboard';
  import { describeError } from '$lib/errors';
  import { buildRunQuery, formatDuration, statusClass } from '$lib/runs';
  import { session } from '$lib/session.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Card from '$lib/components/Card.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import RunBars from '$lib/components/RunBars.svelte';
  import FormError from '$lib/components/FormError.svelte';
  import { btn } from '$lib/ui';

  const canRuns = $derived(session.can('run.read'));
  const canFleet = $derived(session.can('fleet.read'));
  const canConnectors = $derived(session.can('connector.read'));

  let runs = $state<Run[]>([]);
  let hosts = $state<FleetHost[]>([]);
  let alerts = $state<FleetAlert[]>([]);
  let connectors = $state<Connector[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let now = $state(Date.now());

  async function load(ws: string) {
    const jobs: Promise<void>[] = [];
    if (canRuns) {
      jobs.push(
        api.runs.list(ws, buildRunQuery({ limit: 100 })).then((r) => void (runs = r.items ?? [])),
      );
    }
    if (canFleet) {
      jobs.push(api.fleet.hosts(ws).then((h) => void (hosts = h ?? [])));
      jobs.push(api.fleet.alerts(ws, { state: 'open' }).then((a) => void (alerts = a ?? [])));
    }
    if (canConnectors) {
      jobs.push(api.connectors.list(ws).then((c) => void (connectors = c ?? [])));
    }
    try {
      await Promise.all(jobs);
      error = null;
    } catch (e) {
      error = describeError(e);
    } finally {
      now = Date.now();
      loading = false;
    }
  }

  $effect(() => {
    const ws = session.slug;
    if (!ws) return;
    void load(ws);
    // Keep the numbers fresh without a live stream.
    const t = setInterval(() => void load(ws), 30_000);
    return () => clearInterval(t);
  });

  const stats = $derived(runStats(runs, now));
  const fleet = $derived(fleetStats(hosts));
  const health = $derived(connectorHealth(connectors));
  const failing = $derived(failingTemplates(runs));
  const outcomes = $derived(recentOutcomes(runs));
  const recent = $derived(runs.slice(0, 8));

  const tile =
    'rise rounded-2xl border border-border bg-surface p-4 shadow-card hover:border-border-strong';
</script>

<svelte:head><title>Dashboard - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Dashboard"
  description="What ran, what failed and what needs attention in this workspace."
>
  {#snippet actions()}
    {#if canRuns}<a class={btn} href="/runs">All runs</a>{/if}
  {/snippet}
</PageHeader>

<FormError id="dashboard-error" message={error} />

{#if !canRuns && !canFleet && !canConnectors}
  <EmptyState
    title="Nothing to show yet"
    hint="Your role has no read access to runs, fleet or connectors in this workspace."
  />
{:else}
  <section
    aria-label="Key figures"
    class="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6"
  >
    {#if loading}
      {#each [0, 1, 2, 3, 4, 5] as i (i)}<Skeleton class="h-28" />{/each}
    {:else}
      {#if canRuns}
        <a class="{tile} col-span-2 xl:col-span-2" href="/runs">
          <p class="text-xs font-semibold tracking-wide text-muted uppercase">Runs, last 24 h</p>
          <p class="mt-2 flex items-baseline gap-3">
            <span class="text-brand text-4xl font-semibold">{stats.ok}</span>
            <span class="text-sm text-muted">succeeded</span>
            <span class="text-2xl font-semibold {stats.failed > 0 ? 'text-bad' : 'text-muted'}"
              >{stats.failed}</span
            >
            <span class="text-sm text-muted">failed</span>
          </p>
          <div class="mt-2"><RunBars {outcomes} /></div>
        </a>
        <a class={tile} href="/runs?status=running">
          <p class="text-xs font-semibold tracking-wide text-muted uppercase">Running now</p>
          <p class="mt-2 text-3xl font-semibold">{stats.running}</p>
          <p class="mt-1 text-sm text-muted">{stats.queued} queued</p>
        </a>
      {/if}
      {#if canFleet}
        <a class={tile} href="/fleet">
          <p class="text-xs font-semibold tracking-wide text-muted uppercase">Hosts online</p>
          <p class="mt-2 text-3xl font-semibold">
            {fleet.online}<span class="text-lg font-normal text-muted"> / {fleet.total}</span>
          </p>
          <p class="mt-1 text-sm text-muted">{fleet.unmanaged} not managed</p>
        </a>
        <a class={tile} href="/fleet/alerts">
          <p class="text-xs font-semibold tracking-wide text-muted uppercase">Open alerts</p>
          <p class="mt-2 text-3xl font-semibold {alerts.length > 0 ? 'text-warn' : ''}">
            {alerts.length}
          </p>
          <p class="mt-1 text-sm text-muted">from monitoring</p>
        </a>
        <a class={tile} href="/fleet">
          <p class="text-xs font-semibold tracking-wide text-muted uppercase">Drift</p>
          <p class="mt-2 text-3xl font-semibold {fleet.drift > 0 ? 'text-warn' : ''}">
            {fleet.drift}
          </p>
          <p class="mt-1 text-sm text-muted">hosts with changes</p>
        </a>
      {/if}
    {/if}
  </section>

  <div class="grid gap-6 xl:grid-cols-3">
    {#if canRuns}
      <div class="xl:col-span-2">
        <Card title="Recent runs" padded={false}>
          {#snippet actions()}<a class="text-sm text-accent hover:underline" href="/runs"
              >View all</a
            >{/snippet}
          {#if loading}
            <div class="space-y-3 p-4">
              {#each [0, 1, 2, 3] as i (i)}<Skeleton class="h-10" />{/each}
            </div>
          {:else if recent.length === 0}
            <div class="p-4">
              <EmptyState title="No runs yet" hint="Start one from a template." />
            </div>
          {:else}
            <ul class="divide-y divide-border">
              {#each recent as r (r.id)}
                <li>
                  <a
                    href="/runs/{r.id}"
                    class="flex min-h-14 flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 hover:bg-surface-2 sm:px-5"
                  >
                    <span class={statusClass(r.status)}>{r.status}</span>
                    <span class="min-w-0 flex-1 basis-32 truncate font-medium"
                      >{r.template_name}</span
                    >
                    <span class="w-full text-sm text-muted sm:w-auto">
                      {formatDuration(r.started_at, r.finished_at, now)}
                      <span aria-hidden="true"> · </span>
                      {relativeTime(r.created_at, now)}
                    </span>
                  </a>
                </li>
              {/each}
            </ul>
          {/if}
        </Card>
      </div>
    {/if}

    <div class="space-y-6 {canRuns ? '' : 'xl:col-span-3'}">
      {#if canRuns}
        <Card title="Failing templates">
          {#if loading}
            <Skeleton class="h-16" />
          {:else if failing.length === 0}
            <p class="text-sm text-muted">No failed runs among the latest {runs.length}. Nice.</p>
          {:else}
            <ul class="space-y-2">
              {#each failing as f (f.name)}
                <li class="flex items-center justify-between gap-3">
                  <a class="min-w-0 truncate hover:underline" href="/runs/{f.lastRunId}">{f.name}</a
                  >
                  <span class="tone-bad">{f.failures} failed</span>
                </li>
              {/each}
            </ul>
          {/if}
        </Card>
      {/if}

      {#if canConnectors}
        <Card title="Connector health">
          {#snippet actions()}<a class="text-sm text-accent hover:underline" href="/connectors"
              >Manage</a
            >{/snippet}
          {#if loading}
            <Skeleton class="h-16" />
          {:else if connectors.length === 0}
            <p class="text-sm text-muted">No connectors configured.</p>
          {:else}
            <div class="flex flex-wrap gap-2">
              <span class="tone-ok">{health.ok} ok</span>
              {#if health.error > 0}<span class="tone-bad">{health.error} error</span>{/if}
              {#if health.pending > 0}<span class="tone-warn">{health.pending} pending</span>{/if}
              {#if health.disabled > 0}<span class="tone-neutral">{health.disabled} disabled</span
                >{/if}
              {#if health.warnings > 0}<span class="tone-warn">{health.warnings} warnings</span
                >{/if}
            </div>
            {#if health.error > 0}
              <ul class="mt-3 space-y-1 text-sm">
                {#each connectors.filter((c) => c.enabled && c.status === 'error') as c (c.id)}
                  <li class="truncate"><span class="text-bad">{c.name}</span></li>
                {/each}
              </ul>
            {/if}
          {/if}
        </Card>
      {/if}
    </div>
  </div>
{/if}
