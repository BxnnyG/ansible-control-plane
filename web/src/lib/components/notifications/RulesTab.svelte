<script lang="ts">
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { toasts } from '$lib/toasts.svelte';
  import { api, type NotificationChannel, type NotificationRule, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { eventLabel } from '$lib/notifications';
  import { btn, btnDanger, btnPrimary, card, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import RuleForm from './RuleForm.svelte';

  let {
    ws,
    rules,
    channels,
    templates,
    canManage,
    reload,
  }: {
    ws: string;
    rules: NotificationRule[];
    channels: NotificationChannel[];
    templates: Template[];
    canManage: boolean;
    reload: () => Promise<void>;
  } = $props();

  let editing = $state<NotificationRule | 'new' | null>(null);
  let deleting = $state<NotificationRule | null>(null);
  let error = $state<string | null>(null);

  const channelName = (id: string) => channels.find((c) => c.id === id)?.name ?? id;
  const templateName = (id: string | null) =>
    id ? (templates.find((t) => t.id === id)?.name ?? id) : 'All templates';

  async function confirmDelete() {
    if (!deleting) return;
    try {
      await api.notifications.rules.remove(ws, deleting.id);
      toasts.success('Notification rule deleted.');
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
</script>

{#if canManage && editing === null}
  {#if channels.length === 0}
    <p class="mb-4 text-sm text-muted">Create a channel before adding rules.</p>
  {:else}
    <div class="mb-4">
      <button class={btnPrimary} onclick={() => (editing = 'new')}>New rule</button>
    </div>
  {/if}
{/if}
<FormError id="rules-error" message={error} />

{#if editing !== null}
  <div class="{card} mb-6 max-w-xl">
    {#key editing === 'new' ? 'new' : editing.id}
      <RuleForm
        {ws}
        {channels}
        {templates}
        rule={editing === 'new' ? null : editing}
        ondone={done}
        oncancel={() => (editing = null)}
      />
    {/key}
  </div>
{/if}

{#if deleting}
  <ConfirmDialog
    title="Confirm delete"
    onconfirm={confirmDelete}
    oncancel={() => (deleting = null)}
  >
    <p>Delete this rule for channel <strong>{channelName(deleting.channel_id)}</strong>?</p>
  </ConfirmDialog>
{/if}

{#if rules.length === 0}
  <EmptyState
    title="No rules yet."
    hint="Without a rule no notification is sent. A rule picks events and a channel."
  >
    {#if canManage}
      <button class={btnPrimary} onclick={() => (editing = 'new')}>New rule</button>
    {/if}
  </EmptyState>
{:else}
  <div class="tbl-wrap">
    <table class="w-full">
      <thead class="bg-surface-2">
        <tr>
          <th class={th}>Channel</th><th class={th}>Events</th><th class={th}>Template</th>
          <th class={th}>Status</th><th class={th}><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        {#each rules as r (r.id)}
          <tr>
            <td class={td}>{channelName(r.channel_id)}</td>
            <td class={td}>{r.events.map(eventLabel).join(', ')}</td>
            <td class={td}>{templateName(r.template_id)}</td>
            <td class={td}>{r.enabled ? 'Enabled' : 'Disabled'}</td>
            <td class="{td} text-right whitespace-nowrap">
              {#if canManage}
                <button class={btn} onclick={() => (editing = r)}>Edit</button>
                <button class={btnDanger} onclick={() => (deleting = r)}>Delete</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
