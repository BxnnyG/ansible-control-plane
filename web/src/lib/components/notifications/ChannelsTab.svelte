<script lang="ts">
  import { toasts } from '$lib/toasts.svelte';
  import { api, type NotificationChannel, type NotificationRule, type SecretMeta } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { channelsWithoutRules, typeLabel } from '$lib/notifications';
  import { btn, btnDanger, btnPrimary, card, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import ChannelForm from './ChannelForm.svelte';

  let {
    ws,
    channels,
    rules,
    secrets,
    canManage,
    reload,
    onrules,
  }: {
    ws: string;
    channels: NotificationChannel[];
    rules: NotificationRule[];
    secrets: SecretMeta[];
    canManage: boolean;
    reload: () => Promise<void>;
    /** Switches to the rules tab. */
    onrules: () => void;
  } = $props();

  let editing = $state<NotificationChannel | 'new' | null>(null);
  let deleting = $state<NotificationChannel | null>(null);
  let busyId = $state<string | null>(null);
  let error = $state<string | null>(null);

  async function test(c: NotificationChannel) {
    busyId = c.id;
    error = null;
    try {
      const r = await api.notifications.channels.test(ws, c.id);
      if (r.ok) toasts.success(`${c.name}: test message sent.`);
      else toasts.error(`${c.name}: test failed: ${r.error}`);
    } catch (e) {
      error = describeError(e);
    } finally {
      busyId = null;
    }
  }

  async function confirmDelete() {
    if (!deleting) return;
    try {
      await api.notifications.channels.remove(ws, deleting.id);
      toasts.success('Notification channel deleted.');
      deleting = null;
      await reload();
    } catch (e) {
      error = describeError(e);
      deleting = null;
    }
  }

  async function done() {
    editing = null;
    await reload();
  }

  const unused = $derived(channelsWithoutRules(channels, rules));

  function secretName(id: string | null): string {
    return id ? (secrets.find((s) => s.id === id)?.name ?? 'unknown secret') : '-';
  }
</script>

{#if canManage && editing === null}
  <div class="mb-4">
    <button class={btnPrimary} onclick={() => (editing = 'new')}>New channel</button>
  </div>
{/if}
<FormError id="channels-error" message={error} />
{#if canManage && editing === null && unused.length > 0}
  <p role="status" class="mb-3 rounded border border-border bg-surface-2 p-3 text-sm">
    {unused.map((c) => c.name).join(', ')}
    {unused.length === 1 ? 'has' : 'have'} no rule yet and will not send anything automatically.
    <button type="button" class="underline" onclick={onrules}>Create a rule</button>
  </p>
{/if}

{#if editing !== null}
  <div class="{card} mb-6 max-w-xl">
    {#key editing === 'new' ? 'new' : editing.id}
      <ChannelForm
        {ws}
        {secrets}
        channel={editing === 'new' ? null : editing}
        ondone={done}
        oncancel={() => (editing = null)}
      />
    {/key}
  </div>
{/if}

{#if deleting}
  <div role="alertdialog" aria-label="Confirm delete" class="{card} mb-6 max-w-xl">
    <p>
      Delete channel <strong>{deleting.name}</strong>? Its rules and delivery history are removed.
    </p>
    <div class="mt-3 flex gap-2">
      <button class={btnDanger} onclick={confirmDelete}>Delete</button>
      <button class={btn} onclick={() => (deleting = null)}>Cancel</button>
    </div>
  </div>
{/if}

{#if channels.length === 0}
  <div class="empty">No channels yet.</div>
{:else}
  <div class="tbl-wrap">
    <table class="w-full">
      <thead class="bg-surface-2">
        <tr>
          <th class={th}>Name</th><th class={th}>Type</th><th class={th}>Credential</th>
          <th class={th}>Status</th><th class={th}><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        {#each channels as c (c.id)}
          <tr>
            <td class={td}>{c.name}</td>
            <td class={td}>{typeLabel(c.type)}</td>
            <td class={td}>{secretName(c.secret_id)}</td>
            <td class={td}>{c.enabled ? 'Enabled' : 'Disabled'}</td>
            <td class="{td} text-right whitespace-nowrap">
              {#if canManage}
                <button class={btn} disabled={busyId === c.id} onclick={() => test(c)}>Test</button>
                <button class={btn} onclick={() => (editing = c)}>Edit</button>
                <button class={btnDanger} onclick={() => (deleting = c)}>Delete</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
