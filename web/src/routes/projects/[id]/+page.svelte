<script lang="ts">
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, ApiError, type Project, type ProjectRevision, type SecretMeta } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { normalizeScan } from '$lib/scan';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, card, fmtDate, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import HostKeyPanel from '$lib/components/projects/HostKeyPanel.svelte';
  import ProjectForm from '$lib/components/projects/ProjectForm.svelte';
  import ScanView from '$lib/components/projects/ScanView.svelte';
  import SyncBadge from '$lib/components/projects/SyncBadge.svelte';
  import VaultPanel from '$lib/components/vault/VaultPanel.svelte';
  import WebhookPanel from '$lib/components/projects/WebhookPanel.svelte';

  const id = $derived(page.params.id ?? '');
  const ws = $derived(session.slug);
  const canWrite = $derived(session.can('project.write'));
  const canSync = $derived(session.can('project.sync'));

  let project = $state<Project | null>(null);
  let secrets = $state<SecretMeta[]>([]);
  let revisions = $state<ProjectRevision[]>([]);
  let latest = $state<ProjectRevision | null>(null);
  let error = $state<string | null>(null);
  let notice = $state<string | null>(null);
  let busy = $state(false);
  let confirmDelete = $state(false);

  async function load(slug: string, pid: string) {
    error = null;
    try {
      project = await api.projects.get(slug, pid);
      secrets = session.can('secrets.meta.read') ? await api.secrets.list(slug) : [];
      await loadRevisions(slug, pid);
    } catch (e) {
      error = describeError(e);
    }
  }

  async function loadRevisions(slug: string, pid: string) {
    revisions = await api.projects.revisions(slug, pid);
    try {
      latest = await api.projects.latestRevision(slug, pid);
    } catch (e) {
      // 404 simply means the project was never synced.
      if (!(e instanceof ApiError && e.status === 404)) throw e;
      latest = null;
    }
  }

  $effect(() => {
    if (ws && id) void load(ws, id);
  });

  async function act(label: string, fn: () => Promise<unknown>) {
    if (!ws) return;
    busy = true;
    error = null;
    notice = null;
    try {
      await fn();
      notice = label;
      project = await api.projects.get(ws, id);
      await loadRevisions(ws, id);
    } catch (e) {
      error = describeError(e);
      if (ws) project = await api.projects.get(ws, id).catch(() => project);
    } finally {
      busy = false;
    }
  }

  async function test() {
    if (!ws) return;
    await act('Connection test succeeded.', async () => {
      const r = await api.projects.test(ws, id);
      if (!r.ok) throw new Error('test failed');
    });
  }

  async function remove() {
    if (!ws) return;
    try {
      await api.projects.remove(ws, id);
      await goto('/projects');
    } catch (e) {
      error = describeError(e);
      confirmDelete = false;
    }
  }
</script>

<svelte:head><title>{project?.name ?? 'Project'} - Ansible Control Plane</title></svelte:head>

<p class="mb-2 text-sm"><a class="text-accent hover:underline" href="/projects">Projects</a></p>

{#if project && ws}
  <header class="mb-6 flex flex-wrap items-start justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold sm:text-3xl">{project.name}</h1>
      <p class="mt-1 text-sm break-all text-muted">{project.url}</p>
      <p class="mt-1 text-sm">
        <SyncBadge status={project.last_sync_status} />
        <span class="text-muted">{fmtDate(project.last_sync_at)}</span>
        {#if project.last_sync_sha}<code class="text-xs">{project.last_sync_sha.slice(0, 8)}</code
          >{/if}
      </p>
      {#if project.last_sync_status === 'error' && project.last_sync_error}
        <p class="mt-1 text-sm text-bad">{project.last_sync_error}</p>
      {/if}
    </div>
    <div class="flex flex-wrap gap-2">
      {#if canSync}
        <button class={btn} disabled={busy} onclick={test}>Test connection</button>
        <button
          class={btn}
          disabled={busy}
          onclick={() => act('Sync finished.', () => api.projects.sync(ws, id))}
        >
          Sync now
        </button>
      {/if}
      {#if canWrite}
        <button class={btnDanger} onclick={() => (confirmDelete = true)}>Delete</button>
      {/if}
    </div>
  </header>

  {#if notice}<p role="status" class="mb-3 text-sm text-ok">
      {notice}
    </p>{/if}
  <FormError id="project-detail-error" message={error} />

  {#if confirmDelete}
    <div role="alertdialog" aria-label="Confirm delete" class="{card} mb-6 max-w-xl">
      <p>Delete project <strong>{project.name}</strong> and its synced data?</p>
      <div class="mt-3 flex gap-2">
        <button class={btnDanger} onclick={remove}>Delete</button>
        <button class={btn} onclick={() => (confirmDelete = false)}>Cancel</button>
      </div>
    </div>
  {/if}

  <div class="grid gap-6 lg:grid-cols-2">
    <section class={card} aria-labelledby="settings-h">
      <h2 id="settings-h" class="mb-3 text-lg font-medium">Settings</h2>
      {#if canWrite}
        {#key project.updated_at}
          <ProjectForm
            {ws}
            {project}
            {secrets}
            ondone={(p) => {
              project = p;
              notice = 'Saved.';
            }}
          />
        {/key}
      {:else}
        <p class="text-sm text-muted">Read-only.</p>
      {/if}
    </section>

    <div class="space-y-6">
      {#if project.ssh}
        <HostKeyPanel {ws} {project} {canWrite} onchange={(p) => (project = p)} />
      {/if}
      <WebhookPanel {ws} projectId={id} {canWrite} />
    </div>
  </div>

  <section class="mt-6" aria-labelledby="rev-h">
    <h2 id="rev-h" class="mb-2 text-lg font-medium">Revisions</h2>
    {#if revisions.length === 0}
      <p class="text-sm text-muted">No revisions yet. Run a sync.</p>
    {:else}
      <div class="tbl-wrap">
        <table class="w-full">
          <thead class="bg-surface-2">
            <tr><th class={th}>Commit</th><th class={th}>Synced</th><th class={th}>Warnings</th></tr
            >
          </thead>
          <tbody class="divide-y divide-border">
            {#each revisions as r (r.id)}
              <tr>
                <td class={td}><code>{r.sha.slice(0, 12)}</code></td>
                <td class={td}>{fmtDate(r.synced_at)}</td>
                <td class={td}>{r.warnings?.length ?? 0}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  {#if session.can('vault.read') && latest}
    <div class="mt-6">
      <VaultPanel
        {ws}
        projectId={id}
        canManage={session.can('vault.manage')}
        canDecrypt={session.can('vault.decrypt')}
      />
    </div>
  {/if}

  {#if latest}
    <div class="mt-6"><ScanView scan={normalizeScan(latest.scan)} /></div>
  {/if}
{:else if !error}
  <ListSkeleton />
{:else}
  <FormError id="project-detail-error" message={error} />
{/if}
