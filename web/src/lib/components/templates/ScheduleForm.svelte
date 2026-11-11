<script lang="ts">
  import { api, type Schedule, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import {
    CRON_PRESETS,
    emptyScheduleForm,
    presetFor,
    timeZoneOptions,
    toScheduleInput,
    validateSchedule,
    type ScheduleForm,
  } from '$lib/schedules';
  import { buildStartRequest, startFormFromOverrides } from '$lib/runStart';
  import { surveyToFormFields } from '$lib/survey';
  import { surveyFields } from '$lib/templates';
  import OverrideFields from './OverrideFields.svelte';
  import { btn, btnPrimary, fmtDate, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    templateId,
    template,
    schedule = null,
    ondone,
    oncancel,
  }: {
    ws: string;
    templateId: string;
    template: Template;
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
  // svelte-ignore state_referenced_locally
  const fields = surveyToFormFields(surveyFields(template.survey));
  // svelte-ignore state_referenced_locally
  let ovForm = $state(startFormFromOverrides(template, fields, schedule?.overrides));
  let ovErrors = $state<string[]>([]);
  const hasOverrides = $derived(
    template.allow_limit_override ||
      template.allow_tags_override ||
      template.allow_vars_override ||
      template.allow_check_override ||
      template.allow_diff_override ||
      template.allow_verbosity_override ||
      fields.length > 0 ||
      ovForm.surveyRaw !== null,
  );
  const zones = timeZoneOptions();
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
    const built = buildStartRequest(template, fields, ovForm);
    ovErrors = built.errors;
    if (Object.keys(fieldErrors).length > 0 || ovErrors.length > 0) return;
    busy = true;
    try {
      const body = { ...toScheduleInput(form), overrides: built.overrides };
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
      {#if fieldErrors.name}<p class="text-xs text-bad">
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
      <div class="mt-1.5 flex flex-wrap gap-1.5" role="group" aria-label="Cron presets">
        {#each CRON_PRESETS as p (p.id)}
          <button
            type="button"
            class="rounded-full border px-2.5 py-0.5 text-xs hover:bg-surface-2 {presetFor(
              form.cron,
            ) === p.id
              ? 'border-border-strong bg-surface-2 font-medium'
              : 'border-border text-muted'}"
            aria-pressed={presetFor(form.cron) === p.id}
            onclick={() => (form.cron = p.cron)}>{p.label}</button
          >
        {/each}
      </div>
      {#if fieldErrors.cron}<p class="text-xs text-bad">
          {fieldErrors.cron}
        </p>{/if}
    </div>
    <div>
      <label class={label} for="s-tz">Time zone</label>
      <input id="s-tz" class={input} bind:value={form.timezone} list="s-tz-list" />
      <datalist id="s-tz-list">
        {#each zones as z (z)}<option value={z}></option>{/each}
      </datalist>
      {#if fieldErrors.timezone}<p class="text-xs text-bad">
          {fieldErrors.timezone}
        </p>{/if}
    </div>
  </div>
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={form.enabled} /> Enabled
  </label>
  {#if hasOverrides}
    <fieldset class="space-y-3 rounded-xl border border-border p-3">
      <legend class="px-1 text-sm font-medium">Run overrides</legend>
      <p class="text-xs text-muted">
        Applied to every scheduled run; values equal to the template defaults are not stored.
      </p>
      {#if template.allow_limit_override}
        <div>
          <label class={label} for="so-limit">Limit</label>
          <input id="so-limit" class={input} bind:value={ovForm.limit} placeholder="all hosts" />
        </div>
      {/if}
      <OverrideFields {template} {fields} bind:form={ovForm} idPrefix="so" />
    </fieldset>
    {#if ovErrors.length > 0}
      <ul role="alert" class="list-disc pl-5 text-sm text-bad">
        {#each ovErrors as m (m)}<li>{m}</li>{/each}
      </ul>
    {/if}
  {/if}
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
