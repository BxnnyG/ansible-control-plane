<script lang="ts">
  import { page } from '$app/state';
  import { api, type RunDetail, type RunStatusEvent } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { formatDuration, isFinal } from '$lib/runs';
  import { session } from '$lib/session.svelte';
  import { btnDanger, card, fmtDate } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import HostResults from '$lib/components/runs/HostResults.svelte';
  import LogViewer from '$lib/components/runs/LogViewer.svelte';
  import StatusBadge from '$lib/components/runs/StatusBadge.svelte';

  const id = $derived(page.params.id ?? '');
  const ws = $derived(session.slug);
  const canCancel = $derived(session.can('run.cancel'));

  let run = $state<RunDetail | null>(null);
  let error = $state<string | null>(null);
  let now = $state(Date.now());
  let busy = $state(false);

  async function load(slug: string, rid: string) {
    error = null;
    try {
      run = await api.runs.get(slug, rid);
    } catch (e) {
      error = describeError(e);
    }
  }

  $effect(() => {
    if (ws && id) void load(ws, id);
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

  const who = $derived(
    !run?.triggered_by
      ? run?.trigger === 'manual' || run?.trigger === 'api'
        ? 'unknown'
        : '-'
      : run.triggered_by === session.me?.id
        ? 'you'
        : run.triggered_by.slice(0, 8),
  );
</script>

<svelte:head><title>Run - Ansible Control Plane</title></svelte:head>

<p class="mb-2 text-sm"><a class="text-accent hover:underline" href="/runs">Runs</a></p>
<FormError id="run-page-error" message={error} />

{#if run && ws}
  <header class="mb-4 flex flex-wrap items-start justify-between gap-4">
    <div>
      <h1 class="flex items-center gap-3 text-2xl font-semibold">
        {#if run.template_id}
          <a class="hover:underline" href="/templates/{run.template_id}">{run.template_name}</a>
        {:else}{run.template_name}{/if}
        <StatusBadge status={run.status} />
      </h1>
      <p class="mt-1 font-mono text-xs text-muted">{run.params.playbook}</p>
    </div>
    {#if canCancel && (run.status === 'queued' || run.status === 'running')}
      <button class={btnDanger} disabled={busy || !!run.cancel_requested_at} onclick={cancel}>
        {run.cancel_requested_at ? 'Cancelling...' : 'Cancel run'}
      </button>
    {/if}
  </header>

  <section class="{card} mb-6" aria-label="Run details">
    <dl class="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-3 lg:grid-cols-6">
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
    <h2 id="log-h" class="mb-2 text-lg font-medium">Log</h2>
    <LogViewer {ws} runId={run.id} onstatus={applyStatus} />
  </section>

  <section aria-labelledby="hosts-h">
    <h2 id="hosts-h" class="mb-2 text-lg font-medium">Hosts</h2>
    <HostResults results={run.host_results} />
  </section>
{/if}
