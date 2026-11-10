<script lang="ts">
  import Breadcrumbs from '$lib/components/Breadcrumbs.svelte';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { api, type RunDetail, type RunStatusEvent } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { formatDuration, isFinal, triggeredByLabel } from '$lib/runs';
  import { session } from '$lib/session.svelte';
  import { prepareRunAgain, type RunAgain } from '$lib/runAgain';
  import { btn, btnDanger, card, fmtDate } from '$lib/ui';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import RunDialog from '$lib/components/templates/RunDialog.svelte';
  import FormError from '$lib/components/FormError.svelte';
  import HostResults from '$lib/components/runs/HostResults.svelte';
  import LogViewer from '$lib/components/runs/LogViewer.svelte';
  import StatusBadge from '$lib/components/runs/StatusBadge.svelte';

  const id = $derived(page.params.id ?? '');
  const ws = $derived(session.slug);
  const canCancel = $derived(session.can('run.cancel'));
  const canRun = $derived(session.can('run.execute'));

  let run = $state<RunDetail | null>(null);
  const runId = $derived(run?.id ?? '');
  let error = $state<string | null>(null);
  let now = $state(Date.now());
  let busy = $state(false);
  let again = $state<RunAgain | null>(null);
  let againBusy = $state(false);

  async function runAgain() {
    if (!ws || !run?.template_id) return;
    againBusy = true;
    error = null;
    try {
      again = await prepareRunAgain(ws, run);
    } catch (e) {
      error = describeError(e);
    } finally {
      againBusy = false;
    }
  }

  let loading = $state(true);
  let loadError = $state<string | null>(null);

  async function load(slug: string, rid: string) {
    loadError = null;
    loading = true;
    try {
      const r = await api.runs.get(slug, rid);
      // Ignore a response for a run the page has since navigated away from.
      if (untrack(() => id) === rid) run = r;
    } catch (e) {
      if (untrack(() => id) === rid) loadError = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    const slug = ws;
    const rid = id;
    if (!slug || !rid) return;
    untrack(() => {
      run = null;
      void load(slug, rid);
    });
  });

  // Ticking clock for the duration of a running run.
  $effect(() => {
    if (!run || isFinal(run.status)) return;
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  function applyStatus(e: RunStatusEvent) {
    if (!run) return;
    run = { ...run, ...e };
  }

  async function cancel() {
    if (!ws || !run) return;
    busy = true;
    error = null;
    try {
      const r = await api.runs.cancel(ws, run.id);
      run = { ...run, ...r };
    } catch (e) {
      error = describeError(e);
    } finally {
      busy = false;
    }
  }

  const who = $derived(run ? triggeredByLabel(run, session.me?.id) : '-');
</script>

<svelte:head><title>Run - Ansible Control Plane</title></svelte:head>

<Breadcrumbs trail={[{ label: 'Runs', href: '/runs' }]} />
<FormError id="run-page-error" message={run ? error : (loadError ?? error)} />

{#if !run}
  {#if loading}
    <div class="space-y-4" aria-busy="true" aria-label="Loading run">
      <Skeleton class="h-7 w-64" />
      <div class="{card} space-y-3">
        <Skeleton class="h-4 w-full" />
        <Skeleton class="h-4 w-3/4" />
        <Skeleton class="h-4 w-1/2" />
      </div>
      <Skeleton class="h-48 w-full" />
    </div>
  {:else if loadError}
    <button class={btn} onclick={() => ws && load(ws, id)}>Retry</button>
  {/if}
{/if}

{#if run && ws}
  <header
    class="mb-5 flex flex-wrap items-end justify-between gap-x-4 gap-y-3 border-b border-border pb-4"
  >
    <div>
      <h1 class="flex items-center gap-3 text-xl font-semibold">
        {#if run.template_id}
          <a class="hover:underline" href="/templates/{run.template_id}">{run.template_name}</a>
        {:else}{run.template_name}{/if}
        <StatusBadge status={run.status} />
      </h1>
      <p class="mt-1 font-mono text-xs text-muted">{run.params.playbook}</p>
    </div>
    <div class="flex flex-wrap gap-2">
      {#if canRun && run.template_id}
        <button class={btn} disabled={againBusy} onclick={runAgain}>
          {againBusy ? 'Loading...' : 'Run again'}
        </button>
      {/if}
      {#if canCancel && (run.status === 'queued' || run.status === 'running')}
        <button class={btnDanger} disabled={busy || !!run.cancel_requested_at} onclick={cancel}>
          {run.cancel_requested_at ? 'Cancelling...' : 'Cancel run'}
        </button>
      {/if}
    </div>
  </header>

  <section class="{card} mb-6" aria-label="Run details">
    <dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3 lg:grid-cols-6">
      <div>
        <dt class="text-muted">Trigger</dt>
        <dd>{run.trigger}</dd>
      </div>
      <div>
        <dt class="text-muted">User</dt>
        <dd>{who}</dd>
      </div>
      <div>
        <dt class="text-muted">Revision</dt>
        <dd class="font-mono">{run.project_revision.slice(0, 8) || '-'}</dd>
      </div>
      <div>
        <dt class="text-muted">Duration</dt>
        <dd>{formatDuration(run.started_at, run.finished_at, now)}</dd>
      </div>
      <div>
        <dt class="text-muted">Created</dt>
        <dd>{fmtDate(run.created_at)}</dd>
      </div>
      <div>
        <dt class="text-muted">Exit code</dt>
        <dd>{run.exit_code ?? '-'}</dd>
      </div>
      <div>
        <dt class="text-muted">Limit</dt>
        <dd>{run.params.limit || 'all'}</dd>
      </div>
      <div>
        <dt class="text-muted">Hosts</dt>
        <dd>{(run.params.hosts ?? []).length}</dd>
      </div>
      <div>
        <dt class="text-muted">Mode</dt>
        <dd>{run.params.check ? 'check' : 'apply'}{run.params.diff ? ', diff' : ''}</dd>
      </div>
      <div>
        <dt class="text-muted">Tags</dt>
        <dd>{run.params.tags || '-'}</dd>
      </div>
      <div>
        <dt class="text-muted">Skip tags</dt>
        <dd>{run.params.skip_tags || '-'}</dd>
      </div>
      <div>
        <dt class="text-muted">Summary</dt>
        <dd>
          {run.hosts_ok} ok, {run.hosts_changed} changed, {run.hosts_failed} failed, {run.hosts_unreachable}
          unreachable
        </dd>
      </div>
    </dl>
  </section>

  <section class="mb-6" aria-labelledby="log-h">
    <h2 id="log-h" class="mb-2 text-sm font-semibold">Log</h2>
    <LogViewer {ws} {runId} onstatus={applyStatus} />
  </section>

  <section aria-labelledby="hosts-h">
    <h2 id="hosts-h" class="mb-2 text-sm font-semibold">Hosts</h2>
    <HostResults results={run.host_results} />
  </section>
{/if}

{#if again && ws}
  <RunDialog
    {ws}
    template={again.template}
    prefill={again.prefill}
    notCarried={again.notCarried}
    onclose={() => (again = null)}
  />
{/if}
