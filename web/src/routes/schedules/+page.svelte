<script lang="ts">
  import { untrack } from 'svelte';
  import { api, type Schedule, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { scheduleRows } from '$lib/schedules';
  import { sortRows, type SortState } from '$lib/sort';
  import { session } from '$lib/session.svelte';
  import { toasts } from '$lib/toasts.svelte';
  import { btn, fmtDate, td } from '$lib/ui';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import FormError from '$lib/components/FormError.svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SortHeader from '$lib/components/SortHeader.svelte';

  let schedules = $state<Schedule[]>([]);
  let templates = $state<Template[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let busyId = $state<string | null>(null);
  let sort = $state<SortState | null>(null);

  const canRead = $derived(session.can('schedule.read'));
  const canWrite = $derived(session.can('schedule.write'));
  const joined = $derived(scheduleRows(schedules, templates));
  const rows = $derived(
    sortRows(joined, sort, {
      name: (r) => r.schedule.name,
      template: (r) => r.templateName,
      cron: (r) => r.schedule.cron,
      next: (r) => (r.schedule.enabled ? r.schedule.next_run_at : null),
      last: (r) => r.schedule.last_run_at,
      state: (r) => (r.schedule.enabled ? 'enabled' : 'disabled'),
    }),
  );

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      // Template names only decorate the rows; the schedules list shows first.
      const namesP = session.can('template.read')
        ? api.templates
            .list(ws, (d) => {
              if (session.slug === ws) templates = d ?? [];
            })
            .then((d) => {
              if (session.slug === ws) templates = d ?? [];
            })
        : Promise.resolve();
      namesP.catch(() => undefined);
      const list = await api.schedules.list(ws, (d) => {
        if (session.slug === ws) schedules = d ?? [];
      });
      if (session.slug === ws) schedules = list ?? [];
      loading = false;
      await namesP;
    } catch (e) {
      error = describeError(e);
      loading = false;
    }
  }

  $effect(() => {
    const ws = session.slug;
    if (ws && canRead) untrack(() => void load(ws));
    else loading = false;
  });

  async function toggle(s: Schedule) {
    const ws = session.slug;
    if (!ws) return;
    busyId = s.id;
    try {
      const next = await api.schedules.update(ws, s.id, { enabled: !s.enabled });
      schedules = schedules.map((x) => (x.id === s.id ? next : x));
      toasts.success(next.enabled ? 'Schedule enabled.' : 'Schedule disabled.');
    } catch (e) {
      toasts.error(describeError(e));
    } finally {
      busyId = null;
    }
  }
</script>

<svelte:head><title>Schedules - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Schedules"
  description="Every scheduled run in this workspace. Create and edit schedules on the template page."
/>

<FormError id="schedules-page-error" message={error} />

{#if !canRead}
  <p class="text-muted">You do not have permission to view schedules in this workspace.</p>
{:else if loading}
  <ListSkeleton cols={canWrite ? 7 : 6} />
{:else if error && schedules.length === 0}
  <button class={btn} onclick={() => session.slug && load(session.slug)}>Retry</button>
{:else if schedules.length === 0}
  <EmptyState
    title="No schedules yet"
    hint="Open a template and add a schedule to run it on a cron expression."
  >
    <a class={btn} href="/templates">Go to templates</a>
  </EmptyState>
{:else}
  <div class="tbl-wrap">
    <table class="w-full">
      <thead class="bg-surface-2">
        <tr>
          <SortHeader label="Name" key="name" bind:sort />
          <SortHeader label="Template" key="template" bind:sort />
          <SortHeader label="Cron" key="cron" bind:sort />
          <SortHeader label="Next run" key="next" bind:sort />
          <SortHeader label="Last run" key="last" bind:sort />
          <SortHeader label="State" key="state" bind:sort />
          {#if canWrite}<th></th>{/if}
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        {#each rows as r (r.schedule.id)}
          {@const s = r.schedule}
          <tr>
            <td class={td}>{s.name}</td>
            <td class={td}>
              {#if r.templateName !== '-'}
                <a
                  class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                  href="/templates/{s.template_id}">{r.templateName}</a
                >
              {:else}
                <a class="text-muted underline" href="/templates/{s.template_id}">Template</a>
              {/if}
            </td>
            <td class="{td} font-mono text-xs"
              >{s.cron}<br /><span class="text-muted">{s.timezone}</span></td
            >
            <td class={td}>{s.enabled ? fmtDate(s.next_run_at) : '-'}</td>
            <td class={td}>{fmtDate(s.last_run_at)}</td>
            <td class={td}>{s.enabled ? 'enabled' : 'disabled'}</td>
            {#if canWrite}
              <td class="{td} whitespace-nowrap">
                <button class={btn} disabled={busyId === s.id} onclick={() => toggle(s)}>
                  {s.enabled ? 'Disable' : 'Enable'}
                </button>
              </td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
