<script lang="ts">
  import { api, type ConnectorSync } from '$lib/api';
  import { fmtDuration } from '$lib/connectors';
  import { describeError } from '$lib/errors';
  import { fmtDate, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let { ws, id }: { ws: string; id: string } = $props();

  let syncs = $state<ConnectorSync[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  $effect(() => {
    loading = true;
    error = null;
    api.connectors
      .syncs(ws, id, 20)
      .then((s) => (syncs = s))
      .catch((e) => (error = describeError(e)))
      .finally(() => (loading = false));
  });
</script>

<FormError id="syncs-error" message={error} />
{#if loading}
  <p class="text-sm text-muted">Loading...</p>
{:else if syncs.length === 0}
  <p class="text-sm text-muted">No syncs yet.</p>
{:else}
  <div class="overflow-x-auto rounded-lg border border-border">
    <table class="w-full">
      <caption class="sr-only">Sync history</caption>
      <thead class="bg-surface-2">
        <tr>
          <th class={th}>Started</th><th class={th}>Trigger</th><th class={th}>Status</th>
          <th class={th}>Duration</th><th class={th}>Networks</th><th class={th}>Observations</th>
          <th class={th}>Error</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        {#each syncs as s (s.id)}
          <tr>
            <td class={td}>{fmtDate(s.started_at)}</td>
            <td class={td}>{s.trigger}</td>
            <td class={td}>{s.status}</td>
            <td class={td}>{fmtDuration(s.duration_ms)}</td>
            <td class={td}>{s.counts?.networks ?? 0}</td>
            <td class={td}>{s.counts?.observations ?? 0}</td>
            <td class="{td} text-red-600 dark:text-red-400">{s.error}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
