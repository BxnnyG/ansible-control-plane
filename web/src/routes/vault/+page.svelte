<script lang="ts">
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { api, type SecretMeta, type VaultIdentity } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, btnPrimary, card, fmtDate, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import IdentityForm from '$lib/components/vault/IdentityForm.svelte';

  let items = $state<VaultIdentity[]>([]);
  let secrets = $state<SecretMeta[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let editing = $state<VaultIdentity | 'new' | null>(null);
  let deleting = $state<VaultIdentity | null>(null);

  const canRead = $derived(session.can('vault.read'));
  const canManage = $derived(session.can('vault.manage'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      items = await api.vaultIdentities.list(ws);
      secrets = session.can('secrets.meta.read') ? await api.secrets.list(ws) : [];
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

  function secretName(id: string | null): string {
    if (!id) return '-';
    return secrets.find((s) => s.id === id)?.name ?? id;
  }

  async function confirmDelete() {
    if (!deleting || !session.slug) return;
    try {
      await api.vaultIdentities.remove(session.slug, deleting.id);
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
</script>

<svelte:head><title>Vault identities - Ansible Control Plane</title></svelte:head>

<header class="mb-6 flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
  <div>
    <h1 class="text-2xl font-semibold sm:text-3xl">Vault identities</h1>
    <p class="mt-1 text-muted">
      Named sources for Ansible Vault passwords. The passwords themselves are never shown.
    </p>
  </div>
  {#if canManage && editing === null}
    <button class={btnPrimary} onclick={() => (editing = 'new')}>New identity</button>
  {/if}
</header>

<FormError id="vault-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view vault identities in this workspace.</p>
{:else}
  {#if editing !== null && session.slug}
    <div class="{card} mb-6 max-w-xl">
      {#key editing === 'new' ? 'new' : editing.id}
        <IdentityForm
          ws={session.slug}
          identity={editing === 'new' ? null : editing}
          {secrets}
          ondone={done}
          oncancel={() => (editing = null)}
        />
      {/key}
    </div>
  {/if}

  {#if deleting}
    <div role="alertdialog" aria-label="Confirm delete" class="{card} mb-6 max-w-xl">
      <p>
        Delete vault identity <strong>{deleting.label}</strong>? Operations and runs that use this
        label can no longer obtain the password.
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
    <div class="empty">No vault identities yet.</div>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Label</th><th class={th}>Source</th><th class={th}>Updated</th>
            <th class={th}><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as i (i.id)}
            <tr>
              <td class={td}>
                {i.label}
                {#if i.description}<div class="text-xs text-muted">{i.description}</div>{/if}
              </td>
              <td class={td}>
                {#if i.provider === 'secret'}
                  Secret <code>{secretName(i.secret_id)}</code>
                {:else}
                  Script <code>{i.script_path}</code>
                {/if}
              </td>
              <td class={td}>{fmtDate(i.updated_at)}</td>
              <td class="{td} text-right whitespace-nowrap">
                {#if canManage}
                  <button class={btn} onclick={() => (editing = i)}>Edit</button>
                  <button class={btnDanger} onclick={() => (deleting = i)}>Delete</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
