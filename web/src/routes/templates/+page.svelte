<script lang="ts">
  import SortHeader from '$lib/components/SortHeader.svelte';
  import { sortRows, type SortState } from '$lib/sort';
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, type Run, type Schedule, type Template } from '$lib/api';
  import { relativeTime } from '$lib/dashboard';
  import { describeError } from '$lib/errors';
  import { buildRunQuery } from '$lib/runs';
  import {
    filterTemplates,
    groupSchedules,
    lastRunByTemplate,
    scheduleLabel,
  } from '$lib/templateList';
  import { session } from '$lib/session.svelte';
  import { loadTemplateCredentials, loadTemplateNames, type TemplateRefs } from '$lib/templateData';
  import { btn, btnPrimary, card, input, td, th } from '$lib/ui';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import FormError from '$lib/components/FormError.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatusBadge from '$lib/components/runs/StatusBadge.svelte';
  import TemplateForm from '$lib/components/templates/TemplateForm.svelte';
  import RunDialog from '$lib/components/templates/RunDialog.svelte';

  let items = $state<Template[]>([]);
  let refs = $state<TemplateRefs>({ projects: [], inventories: [], secrets: [], identities: [] });
  let loading = $state(true);
  let error = $state<string | null>(null);
  let creating = $state(false);
  let cloneOf = $state<Template | null>(null);
  let running = $state<Template | null>(null);
  let query = $state('');
  let lastRuns = $state(new Map<string, Run>());
  let schedules = $state<Record<string, Schedule[]>>({});
  let now = $state(Date.now());
  const filtered = $derived(filterTemplates(items, query));
  let sort = $state<SortState | null>(null);
  const rows = $derived(
    sortRows(filtered, sort, {
      name: (t) => t.name,
      playbook: (t) => t.playbook,
      project: (t) => projectName(t.project_id),
      inventory: (t) => inventoryName(t.inventory_id),
      last: (t) => lastRuns.get(t.id)?.created_at,
      schedule: (t) => scheduleLabel(schedules[t.id]),
    }),
  );
  const canSchedules = $derived(session.can('schedule.read'));

  const canRead = $derived(session.can('template.read'));
  const canWrite = $derived(session.can('template.write'));
  const canRun = $derived(session.can('run.execute'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      // Names only decorate rows: the list (possibly cached) shows first, refs fill in after.
      const namesP = loadTemplateNames(ws).then((names) => {
        if (session.slug === ws) refs = { ...refs, ...names };
      });
      namesP.catch(() => undefined);
      const list = await api.templates.list(ws, (d) => {
        if (session.slug === ws) items = d ?? [];
      });
      items = list ?? [];
      loading = false;
      void loadExtras(ws);
      await namesP;
    } catch (e) {
      error = describeError(e);
      loading = false;
    }
  }

  /** Last-run badges and schedule hints fill in after the list is already visible. */
  async function loadExtras(ws: string) {
    now = Date.now();
    if (session.can('run.read')) {
      try {
        const res = await api.runs.list(ws, buildRunQuery({ limit: 100 }));
        if (session.slug === ws) lastRuns = lastRunByTemplate(res.items ?? []);
      } catch {
        // Badges are optional; the list stays usable without them.
      }
    }
    if (session.can('schedule.read')) {
      try {
        const res = await api.schedules.list(ws);
        if (session.slug === ws) schedules = groupSchedules(res ?? []);
      } catch {
        // The schedule column is optional; the list stays usable without it.
      }
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

  // /templates?clone=<id> opens the create form prefilled from that template.
  const cloneId = $derived(page.url.searchParams.get('clone'));
  $effect(() => {
    const ws = session.slug;
    const id = cloneId;
    if (!ws || !id || !canWrite) return;
    untrack(() => {
      api.templates
        .get(ws, id)
        .then(async (src) => {
          if (session.slug !== ws) return;
          cloneOf = src;
          await openForm();
        })
        .catch((e) => {
          error = describeError(e);
        });
    });
  });

  function closeForm() {
    creating = false;
    cloneOf = null;
    if (cloneId) void goto('/templates', { replaceState: true });
  }

  const projectName = (id: string) => refs.projects.find((p) => p.id === id)?.name ?? '-';
  const inventoryName = (id: string) => refs.inventories.find((i) => i.id === id)?.name ?? '-';
</script>

<svelte:head><title>Templates - Ansible Control Plane</title></svelte:head>

<PageHeader title="Templates" description="Reusable playbook run definitions.">
  {#snippet actions()}
    {#if canWrite && !creating}
      <button class={btnPrimary} onclick={openForm}>New template</button>
    {/if}
  {/snippet}
</PageHeader>

<FormError id="templates-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view templates in this workspace.</p>
{:else}
  {#if creating && session.slug}
    <div class="{card} mb-6 max-w-4xl">
      {#key cloneOf?.id}
        <TemplateForm
          ws={session.slug}
          clone={cloneOf}
          {...refs}
          ondone={(t) => goto(`/templates/${t.id}`)}
          oncancel={closeForm}
        />
      {/key}
    </div>
  {/if}

  {#if loading}
    <ListSkeleton cols={5 + (canSchedules ? 1 : 0) + (canRun ? 1 : 0)} />
  {:else if items.length === 0}
    <EmptyState
      title="No templates yet"
      hint="A template ties a playbook to a project and an inventory so it can be run and scheduled."
    >
      {#if canWrite && !creating}
        <button class={btnPrimary} onclick={openForm}>New template</button>
      {/if}
    </EmptyState>
  {:else}
    <div class="mb-3 max-w-sm">
      <input
        class={input}
        type="search"
        bind:value={query}
        placeholder="Search name, playbook or description"
        aria-label="Search templates"
      />
    </div>
    {#if filtered.length === 0}
      <div class="empty">No templates match "{query.trim()}".</div>
    {:else}
      <div class="tbl-wrap">
        <table class="w-full">
          <thead class="bg-surface-2">
            <tr>
              <SortHeader label="Name" key="name" bind:sort /><SortHeader
                label="Playbook"
                key="playbook"
                bind:sort
              /><SortHeader label="Project" key="project" bind:sort />
              <SortHeader label="Inventory" key="inventory" bind:sort /><SortHeader
                label="Last run"
                key="last"
                bind:sort
              />
              {#if canSchedules}<SortHeader label="Schedule" key="schedule" bind:sort />{/if}
              {#if canRun}<th class={th}></th>{/if}
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each rows as t (t.id)}
              {@const last = lastRuns.get(t.id)}
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
                <td class={td}>
                  {#if last}
                    <a href="/runs/{last.id}"><StatusBadge status={last.status} /></a>
                    <span class="text-xs text-muted">{relativeTime(last.created_at, now)}</span>
                  {:else}<span class="text-muted">-</span>{/if}
                </td>
                {#if canSchedules}
                  <td class={td}>{scheduleLabel(schedules[t.id])}</td>
                {/if}
                {#if canRun}
                  <td class={td}>
                    <button class={btn} onclick={() => (running = t)}>Run</button>
                  </td>
                {/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
{/if}

{#if running && session.slug}
  <RunDialog ws={session.slug} template={running} onclose={() => (running = null)} />
{/if}
