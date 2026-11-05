<script lang="ts">
  import { api, type Schedule } from '$lib/api';
  import { describeError } from '$lib/errors';
  import {
    emptyScheduleForm,
    toScheduleInput,
    validateSchedule,
    type ScheduleForm,
  } from '$lib/schedules';
  import { btn, btnPrimary, fmtDate, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    templateId,
    schedule = null,
    ondone,
    oncancel,
  }: {
    ws: string;
    templateId: string;
    schedule?: Schedule | null;
    ondone: () => void;
    oncancel: () => void;
  } = $props();

  // svelte-ignore state_referenced_locally
  let form = $state<ScheduleForm>(
    schedule
      ? {
          name: schedule.name,
          cron: schedule.cron,
          timezone: schedule.timezone,
          enabled: schedule.enabled,
          missedPolicy: schedule.missed_policy,
        }
      : emptyScheduleForm(),
  );
  let fieldErrors = $state<ReturnType<typeof validateSchedule>>({});
  let preview = $state<string[]>([]);
  let previewError = $state<string | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);

  // Debounced server-side preview of the next run times.
  $effect(() => {
    const cron = form.cron;
    const tz = form.timezone;
    const local = validateSchedule({ ...form, name: 'x' });
    if (local.cron || local.timezone) {
      preview = [];
      previewError = local.cron ?? local.timezone ?? null;
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      api.schedules
        .preview(ws, cron.trim(), tz.trim(), 5)
        .then((r) => {
          if (cancelled) return;
          preview = r.next_runs ?? [];
          previewError = null;
        })
        .catch((e) => {
          if (cancelled) return;
          preview = [];
          previewError = describeError(e);
        });
    }, 300);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    fieldErrors = validateSchedule(form);
    if (Object.keys(fieldErrors).length > 0) return;
    busy = true;
    try {
      const body = toScheduleInput(form);
      if (schedule) await api.schedules.update(ws, schedule.id, body);
      else await api.templates.createSchedule(ws, templateId, body);
      ondone();
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="space-y-3" aria-label={schedule ? 'Edit schedule' : 'New schedule'}>
  <div class="grid gap-3 sm:grid-cols-2">
    <div>
      <label class={label} for="s-name">Name</label>
      <input id="s-name" class={input} bind:value={form.name} />
      {#if fieldErrors.name}<p class="text-xs text-red-600 dark:text-red-400">
          {fieldErrors.name}
        </p>{/if}
    </div>
    <div>
      <label class={label} for="s-missed">If runs were missed</label>
      <select id="s-missed" class={input} bind:value={form.missedPolicy}>
        <option value="skip">Skip (wait for the next slot)</option>
        <option value="run_once">Run once</option>
      </select>
    </div>
    <div>
      <label class={label} for="s-cron">Cron expression</label>
      <input id="s-cron" class="{input} font-mono" bind:value={form.cron} />
      {#if fieldErrors.cron}<p class="text-xs text-red-600 dark:text-red-400">
          {fieldErrors.cron}
        </p>{/if}
    </div>
    <div>
      <label class={label} for="s-tz">Time zone</label>
      <input id="s-tz" class={input} bind:value={form.timezone} />
      {#if fieldErrors.timezone}<p class="text-xs text-red-600 dark:text-red-400">
          {fieldErrors.timezone}
        </p>{/if}
    </div>
  </div>
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={form.enabled} /> Enabled
  </label>
  <div class="text-sm" aria-live="polite">
    <span class="font-medium">Next runs</span>
    {#if previewError}
      <p class="text-xs text-muted">{previewError}</p>
    {:else}
      <ul class="text-xs text-muted">
        {#each preview as t (t)}<li>{fmtDate(t)}</li>{/each}
      </ul>
    {/if}
  </div>
  <FormError id="schedule-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{schedule ? 'Save' : 'Create'}</button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
