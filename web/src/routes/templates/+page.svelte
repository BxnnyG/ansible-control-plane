<script lang="ts">
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { goto } from '$app/navigation';
  import { api, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { loadTemplateCredentials, loadTemplateNames, type TemplateRefs } from '$lib/templateData';
  import { btnPrimary, card, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import TemplateForm from '$lib/components/templates/TemplateForm.svelte';
  import RunDialog from '$lib/components/templates/RunDialog.svelte';

  let items = $state<Template[]>([]);
  let refs = $state<TemplateRefs>({ projects: [], inventories: [], secrets: [], identities: [] });
  let loading = $state(true);
  let error = $state<string | null>(null);
  let creating = $state(false);
  let running = $state<Template | null>(null);

  const canRead = $derived(session.can('template.read'));
  const canWrite = $derived(session.can('template.write'));
  const canRun = $derived(session.can('run.execute'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      const [list, names] = await Promise.all([api.templates.list(ws), loadTemplateNames(ws)]);
      items = list ?? [];
      refs = { ...refs, ...names };
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  let credsFor = '';
  /** Secrets and vault identities are only needed by the form: fetch them on first open. */
  async function openForm() {
    creating = true;
    const ws = session.slug;
    if (!ws || credsFor === ws) return;
    credsFor = ws;
    try {
      refs = { ...refs, ...(await loadTemplateCredentials(ws)) };
    } catch (e) {
      credsFor = '';
      error = describeError(e);
    }
  }

  $effect(() => {
    const ws = session.slug;
    if (ws && canRead) untrack(() => void load(ws));
    else loading = false;
  });

  const projectName = (id: string) => refs.projects.find((p) => p.id === id)?.name ?? '-';
  const inventoryName = (id: string) => refs.inventories.find((i) => i.id === id)?.name ?? '-';
</script>

<svelte:head><title>Templates - Ansible Control Plane</title></svelte:head>

<header
  class="mb-5 flex flex-wrap items-end justify-between gap-x-4 gap-y-3 border-b border-border pb-4"
>
  <div>
    <h1 class="text-xl font-semibold">Templates</h1>
    <p class="mt-1 text-muted">Reusable playbook run definitions.</p>
  </div>
  {#if canWrite && !creating}
    <button class={btnPrimary} onclick={openForm}>New template</button>
  {/if}
</header>

<FormError id="templates-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view templates in this workspace.</p>
{:else}
  {#if creating && session.slug}
    <div class="{card} mb-6 max-w-4xl">
      <TemplateForm
        ws={session.slug}
        {...refs}
        ondone={(t) => goto(`/templates/${t.id}`)}
        oncancel={() => (creating = false)}
      />
    </div>
  {/if}

  {#if loading}
    <ListSkeleton />
  {:else if items.length === 0}
    <div class="empty">No templates yet.</div>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Playbook</th><th class={th}>Project</th>
            <th class={th}>Inventory</th>
            {#if canRun}<th class={th}></th>{/if}
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as t (t.id)}
            <tr>
              <td class={td}>
                <a
                  class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                  href="/templates/{t.id}">{t.name}</a
                >
                {#if t.description}<div class="text-xs text-muted">{t.description}</div>{/if}
              </td>
              <td class="{td} font-mono text-xs">{t.playbook}</td>
              <td class={td}>{projectName(t.project_id)}</td>
              <td class={td}>{inventoryName(t.inventory_id)}</td>
              {#if canRun}
                <td class={td}>
                  <button
                    class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                    onclick={() => (running = t)}>Run</button
                  >
                </td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}

{#if running && session.slug}
  <RunDialog ws={session.slug} template={running} onclose={() => (running = null)} />
{/if}
