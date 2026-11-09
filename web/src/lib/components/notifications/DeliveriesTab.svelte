<script lang="ts">
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { api, type NotificationChannel, type NotificationDelivery } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { deliveryStatusClass, eventLabel } from '$lib/notifications';
  import { fmtDate, input, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let { ws, channels }: { ws: string; channels: NotificationChannel[] } = $props();

  let items = $state<NotificationDelivery[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let status = $state<'' | NotificationDelivery['status']>('');
  let channelId = $state('');

  const channelName = (id: string) => channels.find((c) => c.id === id)?.name ?? id;

  $effect(() => {
    const filter = {
      channel_id: channelId || undefined,
      status: status || undefined,
      limit: 100,
    };
    loading = true;
    error = null;
    api.notifications
      .deliveries(ws, filter)
      .then((r) => (items = r))
      .catch((e) => (error = describeError(e)))
      .finally(() => (loading = false));
  });
</script>

<div class="mb-4 flex flex-wrap gap-3">
  <label class="text-sm">
    <span class="mr-1">Channel</span>
    <select class={input} bind:value={channelId}>
      <option value="">All</option>
      {#each channels as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
    </select>
  </label>
  <label class="text-sm">
    <span class="mr-1">Status</span>
    <select class={input} bind:value={status}>
      <option value="">All</option>
      <option value="pending">Pending</option>
      <option value="sent">Sent</option>
      <option value="failed">Failed</option>
    </select>
  </label>
</div>
<FormError id="deliveries-error" message={error} />

{#if loading}
  <ListSkeleton />
{:else if items.length === 0}
  <div class="empty">No deliveries.</div>
{:else}
  <div class="tbl-wrap">
    <table class="w-full">
      <thead class="bg-surface-2">
        <tr>
          <th class={th}>Created</th><th class={th}>Channel</th><th class={th}>Event</th>
          <th class={th}>Status</th><th class={th}>Attempts</th><th class={th}>Last error</th>
          <th class={th}>Run</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        {#each items as d (d.id)}
          <tr>
            <td class={td}>{fmtDate(d.created_at)}</td>
            <td class={td}>{channelName(d.channel_id)}</td>
            <td class={td}>{eventLabel(d.kind)}</td>
            <td class={td}>
              <span class="rounded px-2 py-0.5 text-xs {deliveryStatusClass(d.status)}">
                {d.status}
              </span>
            </td>
            <td class={td}>{d.attempts}</td>
            <td class="{td} max-w-xs text-xs break-words text-bad">
              {d.last_error || '-'}
            </td>
            <td class={td}>
              <a
                class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                href="/runs/{d.run_id}">Open run</a
              >
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
