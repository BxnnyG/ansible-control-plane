<script lang="ts">
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { goto } from '$app/navigation';
  import { api, type Project, type SecretMeta } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { btnPrimary, card, fmtDate, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import ProjectForm from '$lib/components/projects/ProjectForm.svelte';
  import SyncBadge from '$lib/components/projects/SyncBadge.svelte';

  let items = $state<Project[]>([]);
  let secrets = $state<SecretMeta[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let creating = $state(false);

  const canRead = $derived(session.can('project.read'));
  const canWrite = $derived(session.can('project.write'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      items = await api.projects.list(ws, (d) => {
        if (session.slug === ws) items = d;
      });
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
</script>

<svelte:head><title>Projects - Ansible Control Plane</title></svelte:head>

<PageHeader title="Projects" description="Git repositories containing playbooks and roles.">
  {#snippet actions()}
    {#if canWrite && !creating}
      <button class={btnPrimary} onclick={() => (creating = true)}>New project</button>
    {/if}
  {/snippet}
</PageHeader>

<FormError id="projects-error" message={error} onretry={() => session.slug && load(session.slug)} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view projects in this workspace.</p>
{:else}
  {#if creating && session.slug}
    <div class="{card} mb-6 max-w-2xl">
      <ProjectForm
        ws={session.slug}
        {secrets}
        ondone={(p) => goto(`/projects/${p.id}`)}
        oncancel={() => (creating = false)}
      />
    </div>
  {/if}

  {#if loading}
    <ListSkeleton />
  {:else if items.length === 0}
    <EmptyState
      title="No projects yet."
      hint="A project is a Git repository with playbooks and roles."
    >
      {#if canWrite}
        <button class={btnPrimary} onclick={() => (creating = true)}>New project</button>
      {/if}
    </EmptyState>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Repository</th><th class={th}>Last sync</th>
            <th class={th}>Status</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as p (p.id)}
            <tr>
              <td class={td}>
                <a
                  class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                  href="/projects/{p.id}">{p.name}</a
                >
              </td>
              <td class="{td} break-all text-muted">{p.url}</td>
              <td class={td}>
                {fmtDate(p.last_sync_at)}
                {#if p.last_sync_sha}<code class="text-xs text-muted">
                    {p.last_sync_sha.slice(0, 8)}</code
                  >{/if}
              </td>
              <td class={td}>
                <SyncBadge status={p.last_sync_status} />
                {#if p.last_sync_status === 'error' && p.last_sync_error}
                  <div class="mt-1 max-w-xs truncate text-xs text-muted" title={p.last_sync_error}>
                    {p.last_sync_error}
                  </div>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
