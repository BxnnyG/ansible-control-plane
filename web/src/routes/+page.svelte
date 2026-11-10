<script lang="ts">
  import { untrack } from 'svelte';
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
  import { btn, td, th } from '$lib/ui';

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
    // Permissions are read here (tracked) so a workspace switch reloads once they are known.
    void [canRuns, canFleet, canConnectors];
    untrack(() => void load(ws));
    // Keep the numbers fresh without a live stream.
    const t = setInterval(() => untrack(() => void load(ws)), 30_000);
    return () => clearInterval(t);
  });

  const stats = $derived(runStats(runs, now));
  const fleet = $derived(fleetStats(hosts));
  const health = $derived(connectorHealth(connectors));
  const failing = $derived(failingTemplates(runs));
  const outcomes = $derived(recentOutcomes(runs));
  const recent = $derived(runs.slice(0, 8));

  const cell =
    'block rounded-xl border border-border bg-surface p-4 shadow-card hover:-translate-y-px hover:border-border-strong';
  const cellLabel = 'text-[0.68rem] font-semibold tracking-wider text-muted uppercase';
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
    class="mb-6 grid grid-cols-2 gap-3 max-lg:[&>*:last-child:nth-child(odd)]:col-span-2 lg:grid-cols-5"
  >
    {#if loading}
      {#each [0, 1, 2, 3, 4] as i (i)}<div class="rounded-xl border border-border bg-surface p-4">
          <Skeleton class="h-10" />
        </div>{/each}
    {:else}
      {#if canRuns}
        <a class={cell} href="/runs">
          <p class={cellLabel}>Succeeded, 24 h</p>
          <p class="mt-1.5 font-mono text-3xl font-medium text-accent">{stats.ok}</p>
        </a>
        <a class={cell} href="/runs?status=failed">
          <p class={cellLabel}>Failed, 24 h</p>
          <p class="mt-1.5 font-mono text-3xl font-medium {stats.failed > 0 ? 'text-bad' : ''}">
            {stats.failed}
          </p>
        </a>
        <a class={cell} href="/runs?status=running">
          <p class={cellLabel}>Running / queued</p>
          <p class="mt-1.5 font-mono text-3xl font-medium">
            {stats.running}<span class="text-base font-normal text-muted"> / {stats.queued}</span>
          </p>
        </a>
      {/if}
      {#if canFleet}
        <a class={cell} href="/fleet">
          <p class={cellLabel}>Hosts online</p>
          <p class="mt-1.5 font-mono text-3xl font-medium">
            {fleet.online}<span class="text-base font-normal text-muted"> / {fleet.total}</span>
          </p>
          <p class="text-xs text-muted">{fleet.unmanaged} not managed</p>
        </a>
        <a class={cell} href="/fleet/alerts">
          <p class={cellLabel}>Open alerts / drift</p>
          <p class="mt-1.5 font-mono text-3xl font-medium">
            <span class={alerts.length > 0 ? 'text-warn' : ''}>{alerts.length}</span><span
              class="text-base font-normal text-muted"
            >
              /
            </span><span class={fleet.drift > 0 ? 'text-warn' : ''}>{fleet.drift}</span>
          </p>
        </a>
      {/if}
    {/if}
  </section>

  {#if canRuns && !loading && outcomes.length > 0}
    <div class="mb-6 flex items-center gap-3 text-xs text-muted">
      <span class="font-semibold tracking-wider uppercase">Run history</span>
      <RunBars {outcomes} />
    </div>
  {/if}

  <div class="grid gap-6 xl:grid-cols-3">
    {#if canRuns}
      <div class="xl:col-span-2">
        <Card title="Recent runs" padded={false}>
          {#snippet actions()}<a
              class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
              href="/runs">View all</a
            >{/snippet}
          {#if loading}
            <div class="space-y-2 p-3">
              {#each [0, 1, 2, 3] as i (i)}<Skeleton class="h-8" />{/each}
            </div>
          {:else if recent.length === 0}
            <div class="p-3">
              <EmptyState title="No runs yet" hint="Start one from a template." />
            </div>
          {:else}
            <div class="tbl-wrap !max-h-none !rounded-none !border-0 !shadow-none">
              <table class="w-full">
                <thead>
                  <tr>
                    <th class={th}>Status</th>
                    <th class={th}>Template</th>
                    <th class={th}>Duration</th>
                    <th class={th}>Started</th>
                  </tr>
                </thead>
                <tbody>
                  {#each recent as r (r.id)}
                    <tr>
                      <td class={td}><span class={statusClass(r.status)}>{r.status}</span></td>
                      <td class="{td} min-w-0">
                        <a class="font-medium hover:underline" href="/runs/{r.id}"
                          >{r.template_name}</a
                        >
                      </td>
                      <td class="{td} font-mono text-muted">
                        {formatDuration(r.started_at, r.finished_at, now)}
                      </td>
                      <td class="{td} text-muted">{relativeTime(r.created_at, now)}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
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
            <p class="text-sm text-muted">No failed runs among the latest {runs.length}.</p>
          {:else}
            <ul class="space-y-1.5 text-sm">
              {#each failing as f (f.name)}
                <li class="flex items-center justify-between gap-3">
                  <a class="min-w-0 truncate hover:underline" href="/runs/{f.lastRunId}">{f.name}</a
                  >
                  <span class="tone-bad font-mono">{f.failures} failed</span>
                </li>
              {/each}
            </ul>
          {/if}
        </Card>
      {/if}

      {#if canConnectors}
        <Card title="Connector health">
          {#snippet actions()}<a
              class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
              href="/connectors">Manage</a
            >{/snippet}
          {#if loading}
            <Skeleton class="h-16" />
          {:else if connectors.length === 0}
            <p class="text-sm text-muted">No connectors configured.</p>
          {:else}
            <div class="flex flex-wrap gap-x-4 gap-y-1">
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
