<script lang="ts">
  import { untrack } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import FormError from '$lib/components/FormError.svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import ConfigForm from '$lib/components/reachability/ConfigForm.svelte';
  import Matrix from '$lib/components/reachability/Matrix.svelte';
  import SourcesPanel from '$lib/components/reachability/SourcesPanel.svelte';
  import TargetsPanel from '$lib/components/reachability/TargetsPanel.svelte';
  import { api, type ReachabilityMatrix, type SecretMeta } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { toasts } from '$lib/toasts.svelte';
  import { btnPrimary } from '$lib/ui';

  let matrix = $state<ReachabilityMatrix | null>(null);
  let secrets = $state<SecretMeta[]>([]);
  let loading = $state(true);
  let running = $state(false);
  let error = $state<string | null>(null);
  let runId = $state<string | null>(null);
  let now = $state(Date.now());

  const canRead = $derived(session.can('reachability.read'));
  const canWrite = $derived(session.can('reachability.write'));
  const canRun = $derived(session.can('reachability.run'));

  async function load(ws: string) {
    error = null;
    try {
      matrix = await api.reachability.matrix(ws);
      now = Date.now();
      if (canWrite && session.can('secrets.meta.read')) secrets = await api.secrets.list(ws);
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  async function runNow() {
    if (!session.slug) return;
    running = true;
    error = null;
    try {
      const r = await api.reachability.run(session.slug);
      runId = r.id;
      toasts.success('Probe run started. Results appear when it finishes.');
    } catch (e) {
      error = describeError(e);
    } finally {
      running = false;
    }
  }

  $effect(() => {
    const ws = session.slug;
    if (ws && canRead) untrack(() => void load(ws));
    else loading = false;
  });
</script>

<svelte:head><title>Reachability - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Reachability"
  description="Which source host can reach which target: ping, TCP port or DNS lookup."
>
  {#snippet actions()}
    {#if canRun}
      <button class={btnPrimary} disabled={running} onclick={runNow}>Run now</button>
    {/if}
  {/snippet}
</PageHeader>

<FormError
  id="reachability-error"
  message={error}
  onretry={() => session.slug && load(session.slug)}
/>

{#if !canRead}
  <p class="text-muted">You do not have permission to view reachability in this workspace.</p>
{:else if loading}
  <ListSkeleton />
{:else if matrix && session.slug}
  {@const ws = session.slug}
  <div class="space-y-6">
    {#if runId}
      <p class="text-sm">
        Probe run started: <a class="underline" href="/runs/{runId}">open run</a>.
        <button class="underline" onclick={() => session.slug && load(session.slug)}>Refresh</button
        >
      </p>
    {/if}
    <Card title="Matrix" padded={false}>
      <Matrix {matrix} {now} />
    </Card>
    <Card title="Settings" padded={false}>
      {#if canWrite}
        <ConfigForm {ws} config={matrix.config} {secrets} onsaved={() => load(ws)} />
      {:else}
        <p class="p-5 text-sm text-muted">
          Interval: {matrix.config.interval_seconds === 0
            ? 'off'
            : `${matrix.config.interval_seconds} s`}
        </p>
      {/if}
    </Card>
    <Card title="Sources" padded={false}>
      <SourcesPanel {ws} sources={matrix.sources} {canWrite} onchange={() => load(ws)} />
    </Card>
    <Card title="Targets" padded={false}>
      <TargetsPanel {ws} targets={matrix.targets} {canWrite} onchange={() => load(ws)} />
    </Card>
  </div>
{/if}
