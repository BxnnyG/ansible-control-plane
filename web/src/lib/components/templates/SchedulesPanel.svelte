<script lang="ts">
  import { api, type Schedule, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btn, btnDanger, btnPrimary, card, fmtDate, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import ScheduleForm from './ScheduleForm.svelte';

  let {
    ws,
    templateId,
    template,
    canRead,
    canWrite,
  }: { ws: string; templateId: string; template: Template; canRead: boolean; canWrite: boolean } =
    $props();

  let items = $state<Schedule[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let editing = $state<Schedule | 'new' | null>(null);
  let deleting = $state<string | null>(null);

  async function load() {
    error = null;
    try {
      items = (await api.templates.schedules(ws, templateId)) ?? [];
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void templateId;
    if (canRead) void load();
    else loading = false;
  });

  async function act(fn: () => Promise<unknown>) {
    error = null;
    try {
      await fn();
      deleting = null;
      await load();
    } catch (e) {
      error = describeError(e);
    }
  }
</script>

{#if canRead}
  <section class={card} aria-labelledby="sched-h">
    <div class="mb-3 flex items-center justify-between">
      <h2 id="sched-h" class="text-sm font-semibold">Schedules</h2>
      {#if canWrite && !editing}
        <button class={btnPrimary} onclick={() => (editing = 'new')}>New schedule</button>
      {/if}
    </div>
    <FormError id="schedules-error" message={error} />

    {#if editing}
      {#key editing === 'new' ? 'new' : editing.id}
        <div class="mb-4 rounded-md border border-border p-3">
          <ScheduleForm
            {ws}
            {templateId}
            {template}
            schedule={editing === 'new' ? null : editing}
            ondone={() => {
              editing = null;
              void load();
            }}
            oncancel={() => (editing = null)}
          />
        </div>
      {/key}
    {/if}

    {#if loading}
      <p class="text-muted">Loading...</p>
    {:else if items.length === 0}
      <p class="text-sm text-muted">No schedules.</p>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full">
          <thead class="bg-surface-2">
            <tr>
              <th class={th}>Name</th><th class={th}>Cron</th><th class={th}>Next run</th>
              <th class={th}>Last run</th><th class={th}>Missed</th><th class={th}>State</th>
              {#if canWrite}<th class={th}></th>{/if}
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#each items as s (s.id)}
              <tr>
                <td class={td}>{s.name}</td>
                <td class="{td} font-mono text-xs"
                  >{s.cron}<br /><span class="text-muted">{s.timezone}</span></td
                >
                <td class={td}>{s.enabled ? fmtDate(s.next_run_at) : '-'}</td>
                <td class={td}>{fmtDate(s.last_run_at)}</td>
                <td class={td}>{s.missed_policy === 'run_once' ? 'run once' : 'skip'}</td>
                <td class={td}>{s.enabled ? 'enabled' : 'disabled'}</td>
                {#if canWrite}
                  <td class="{td} whitespace-nowrap">
                    <div class="flex gap-1">
                      <button
                        class={btn}
                        onclick={() =>
                          act(() => api.schedules.update(ws, s.id, { enabled: !s.enabled }))}
                      >
                        {s.enabled ? 'Disable' : 'Enable'}
                      </button>
                      <button class={btn} onclick={() => (editing = s)}>Edit</button>
                      {#if deleting === s.id}
                        <button
                          class={btnDanger}
                          onclick={() => act(() => api.schedules.remove(ws, s.id))}>Confirm</button
                        >
                        <button class={btn} onclick={() => (deleting = null)}>No</button>
                      {:else}
                        <button class={btnDanger} onclick={() => (deleting = s.id)}>Delete</button>
                      {/if}
                    </div>
                  </td>
                {/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
{/if}
