<script lang="ts">
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
      items = await api.projects.list(ws);
      secrets = session.can('secrets.meta.read') ? await api.secrets.list(ws) : [];
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
</script>

<svelte:head><title>Projects - Ansible Control Plane</title></svelte:head>

<header class="mb-6 flex items-start justify-between gap-4">
  <div>
    <h1 class="text-2xl font-semibold">Projects</h1>
    <p class="mt-1 text-muted">Git repositories containing playbooks and roles.</p>
  </div>
  {#if canWrite && !creating}
    <button class={btnPrimary} onclick={() => (creating = true)}>New project</button>
  {/if}
</header>

<FormError id="projects-error" message={error} />

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
    <p class="text-muted">Loading...</p>
  {:else if items.length === 0}
    <div class="empty">No projects yet.</div>
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
                <a class="text-accent hover:underline" href="/projects/{p.id}">{p.name}</a>
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
