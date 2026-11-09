<script lang="ts">
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

  const canRead = $derived(session.can('secrets.meta.read'));
  const canWrite = $derived(session.can('secrets.write'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      items = await api.secrets.list(ws);
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (session.slug && canRead) void load(session.slug);
    else loading = false;
  });

  async function confirmDelete() {
    if (!deleting || !session.slug) return;
    try {
      await api.secrets.remove(session.slug, deleting.id);
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

<header class="mb-6 flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
  <div>
    <h1 class="text-2xl font-semibold sm:text-3xl">Secrets</h1>
    <p class="mt-1 text-muted">Credentials used by projects and runs. Values are write-only.</p>
  </div>
  {#if canWrite && editing === null}
    <button class={btnPrimary} onclick={() => (editing = 'new')}>New secret</button>
  {/if}
</header>

<FormError id="secrets-error" message={error} />

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
    <div role="alertdialog" aria-label="Confirm delete" class="{card} mb-6 max-w-xl">
      <p>
        Delete secret <strong>{deleting.name}</strong>? Projects referencing it lose their
        credential.
      </p>
      <div class="mt-3 flex gap-2">
        <button class={btnDanger} onclick={confirmDelete}>Delete</button>
        <button class={btn} onclick={() => (deleting = null)}>Cancel</button>
      </div>
    </div>
  {/if}

  {#if loading}
    <ListSkeleton />
  {:else if items.length === 0}
    <div class="empty">No secrets yet.</div>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Type</th><th class={th}>Fingerprint</th>
            <th class={th}>Updated</th><th class={th}><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as s (s.id)}
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
