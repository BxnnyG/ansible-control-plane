<script lang="ts">
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import SortHeader from '$lib/components/SortHeader.svelte';
  import { sortRows, type SortState } from '$lib/sort';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { toasts } from '$lib/toasts.svelte';
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { api, type SecretMeta } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, btnPrimary, card, fmtDate, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import SecretForm from '$lib/components/secrets/SecretForm.svelte';

  let items = $state<SecretMeta[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let editing = $state<SecretMeta | 'new' | null>(null);
  let deleting = $state<SecretMeta | null>(null);

  let sort = $state<SortState | null>(null);
  const rows = $derived(
    sortRows(items, sort, {
      name: (s) => s.name,
      type: (s) => s.type,
      updated: (s) => s.updated_at,
    }),
  );

  const canRead = $derived(session.can('secrets.meta.read'));
  const canWrite = $derived(session.can('secrets.write'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      items = await api.secrets.list(ws, (d) => {
        if (session.slug === ws) items = d;
      });
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

  async function confirmDelete() {
    if (!deleting || !session.slug) return;
    try {
      await api.secrets.remove(session.slug, deleting.id);
      toasts.success('Secret deleted.');
      deleting = null;
      await load(session.slug);
    } catch (e) {
      error = describeError(e);
      deleting = null;
    }
  }

  function done() {
    editing = null;
    if (session.slug) void load(session.slug);
  }

  function fingerprintOf(s: SecretMeta): string {
    const f = s.metadata?.fingerprint;
    return typeof f === 'string' ? f : '';
  }
</script>

<svelte:head><title>Secrets - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Secrets"
  description="Credentials used by projects and runs. Values are write-only."
>
  {#snippet actions()}
    {#if canWrite && editing === null}
      <button class={btnPrimary} onclick={() => (editing = 'new')}>New secret</button>
    {/if}
  {/snippet}
</PageHeader>

<FormError id="secrets-error" message={error} onretry={() => session.slug && load(session.slug)} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view secrets in this workspace.</p>
{:else}
  {#if editing !== null && session.slug}
    <div class="{card} mb-6 max-w-xl">
      {#key editing === 'new' ? 'new' : editing.id}
        <SecretForm
          ws={session.slug}
          secret={editing === 'new' ? null : editing}
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
      <p>
        Delete secret <strong>{deleting.name}</strong>? Projects referencing it lose their
        credential.
      </p>
    </ConfirmDialog>
  {/if}

  {#if loading}
    <ListSkeleton />
  {:else if items.length === 0}
    <EmptyState
      title="No secrets yet."
      hint="Secrets hold the SSH keys, tokens and passwords that projects and runs use. Values are write-only."
    >
      {#if canWrite}
        <button class={btnPrimary} onclick={() => (editing = 'new')}>New secret</button>
      {/if}
    </EmptyState>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <SortHeader label="Name" key="name" bind:sort /><SortHeader
              label="Type"
              key="type"
              bind:sort
            /><th class={th}>Fingerprint</th>
            <SortHeader label="Updated" key="updated" bind:sort /><th class={th}
              ><span class="sr-only">Actions</span></th
            >
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each rows as s (s.id)}
            <tr>
              <td class={td}>
                {s.name}
                {#if s.description}<div class="text-xs text-muted">{s.description}</div>{/if}
              </td>
              <td class={td}>{s.type}</td>
              <td class="{td} font-mono text-xs break-all">{fingerprintOf(s) || '-'}</td>
              <td class={td}>{fmtDate(s.updated_at)}</td>
              <td class="{td} text-right whitespace-nowrap">
                {#if canWrite}
                  <button class={btn} onclick={() => (editing = s)}>Edit</button>
                  <button class={btnDanger} onclick={() => (deleting = s)}>Delete</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
