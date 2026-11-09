<script lang="ts">
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import {
    api,
    type NotificationChannel,
    type NotificationRule,
    type SecretMeta,
    type Template,
  } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import FormError from '$lib/components/FormError.svelte';
  import ChannelsTab from '$lib/components/notifications/ChannelsTab.svelte';
  import DeliveriesTab from '$lib/components/notifications/DeliveriesTab.svelte';
  import RulesTab from '$lib/components/notifications/RulesTab.svelte';

  type Tab = 'channels' | 'rules' | 'deliveries';
  const TABS: { id: Tab; label: string }[] = [
    { id: 'channels', label: 'Channels' },
    { id: 'rules', label: 'Rules' },
    { id: 'deliveries', label: 'Deliveries' },
  ];

  let tab = $state<Tab>('channels');
  let channels = $state<NotificationChannel[]>([]);
  let rules = $state<NotificationRule[]>([]);
  let secrets = $state<SecretMeta[]>([]);
  let templates = $state<Template[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  const canRead = $derived(session.can('notification.read'));
  const canManage = $derived(session.can('notification.manage'));

  async function load(ws: string) {
    error = null;
    try {
      [channels, rules, templates] = await Promise.all([
        api.notifications.channels.list(ws),
        api.notifications.rules.list(ws),
        api.templates.list(ws),
      ]);
      // The secrets list is only needed (and permitted) for editing.
      secrets = canManage && session.can('secrets.meta.read') ? await api.secrets.list(ws) : [];
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    const ws = session.slug;
    if (ws && canRead) untrack(() => void load(ws));
    else loading = false;
  });
</script>

<svelte:head><title>Notifications - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Notifications"
  description="Send run results (failed, recovered, drift) to webhooks, Matrix, e-mail, Gotify or ntfy."
/>

<FormError id="notifications-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view notifications in this workspace.</p>
{:else if loading}
  <ListSkeleton />
{:else if session.slug}
  <div
    role="tablist"
    class="mb-4 flex gap-1 overflow-x-auto border-b border-border whitespace-nowrap"
  >
    {#each TABS as t (t.id)}
      <button
        role="tab"
        aria-selected={tab === t.id}
        class="-mb-px min-h-11 border-b-2 px-4 py-2 text-sm {tab === t.id
          ? 'border-accent font-semibold text-accent'
          : 'border-transparent text-muted'}"
        onclick={() => (tab = t.id)}>{t.label}</button
      >
    {/each}
  </div>
  {#if tab === 'channels'}
    <ChannelsTab
      ws={session.slug}
      {channels}
      {rules}
      {secrets}
      {canManage}
      onrules={() => (tab = 'rules')}
      reload={() => load(session.slug as string)}
    />
  {:else if tab === 'rules'}
    <RulesTab
      ws={session.slug}
      {rules}
      {channels}
      {templates}
      {canManage}
      reload={() => load(session.slug as string)}
    />
  {:else}
    <DeliveriesTab ws={session.slug} {channels} />
  {/if}
{/if}
