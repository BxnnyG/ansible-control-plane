<script lang="ts">
  import type { StartForm } from '$lib/runStart';
  import {
    selectedChoices,
    surveyToVars,
    toggleChoice,
    varsToSurvey,
    type FormField,
  } from '$lib/survey';
  import { input, label } from '$lib/ui';

  let { fields, form = $bindable() }: { fields: FormField[]; form: StartForm } = $props();

  let note = $state<string | null>(null);

  const raw = $derived(form.surveyRaw !== null);

  function toRaw() {
    note = null;
    form.surveyRaw = JSON.stringify(surveyToVars(fields, form.survey).vars, null, 2);
  }

  function toForm() {
    note = null;
    try {
      const v: unknown = JSON.parse(form.surveyRaw ?? '{}');
      if (v === null || typeof v !== 'object' || Array.isArray(v)) throw new Error('not an object');
      const { survey, rest } = varsToSurvey(fields, v as Record<string, unknown>);
      Object.assign(form.survey, survey);
      const ignored = Object.keys(rest);
      if (ignored.length > 0) note = `Keys not in the survey were dropped: ${ignored.join(', ')}.`;
      form.surveyRaw = null;
    } catch {
      note = 'Raw survey values must be a JSON object before switching back.';
    }
  }
</script>

<fieldset class="space-y-2 rounded-xl border border-border p-3">
  <legend class="px-1 text-sm font-medium">Survey</legend>
  <div class="flex justify-end">
    <button
      type="button"
      class="text-xs text-muted underline"
      aria-pressed={raw}
      onclick={() => (raw ? toForm() : toRaw())}
    >
      {raw ? 'Use form' : 'Advanced: raw JSON'}
    </button>
  </div>
  {#if note}<p class="text-xs text-bad" role="alert">{note}</p>{/if}
  {#if raw}
    <label class={label} for="sv-raw">Survey values (JSON object)</label>
    <textarea id="sv-raw" class="{input} font-mono" rows="5" bind:value={form.surveyRaw}></textarea>
  {:else}
    {#each fields as f (f.key)}
      <div style="margin-left: {f.depth}rem">
        {#if f.group}
          <p class="text-sm font-medium">{f.label}</p>
        {:else if f.kind === 'checkbox'}
          <label class="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={form.survey[f.key] === 'true'}
              onchange={(e) => (form.survey[f.key] = String(e.currentTarget.checked))}
            />
            {f.label}
          </label>
        {:else if f.kind === 'multiselect'}
          <fieldset>
            <legend class={label}>{f.label}{f.required ? ' *' : ''}</legend>
            <div class="flex flex-wrap gap-x-4 gap-y-1">
              {#each f.choices as c (c)}
                <label class="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={selectedChoices(form.survey[f.key] ?? '').includes(c)}
                    onchange={(e) =>
                      (form.survey[f.key] = toggleChoice(
                        form.survey[f.key] ?? '',
                        f.choices,
                        c,
                        e.currentTarget.checked,
                      ))}
                  />
                  {c}
                </label>
              {/each}
            </div>
          </fieldset>
        {:else}
          <label class={label} for="sv-{f.key}">{f.label}{f.required ? ' *' : ''}</label>
          {#if f.kind === 'select'}
            <select id="sv-{f.key}" class={input} bind:value={form.survey[f.key]}>
              {#if !f.required}<option value="">(not set)</option>{/if}
              {#each f.choices as c (c)}<option value={c}>{c}</option>{/each}
            </select>
          {:else if f.kind === 'textarea'}
            <textarea
              id="sv-{f.key}"
              class="{input} font-mono"
              rows="2"
              placeholder={f.type === 'str' ? '' : 'JSON'}
              bind:value={form.survey[f.key]}
            ></textarea>
          {:else if f.kind === 'number'}
            <input
              id="sv-{f.key}"
              class={input}
              type="number"
              step={f.type === 'int' ? 1 : 'any'}
              bind:value={form.survey[f.key]}
            />
          {:else}
            <input
              id="sv-{f.key}"
              class={input}
              type={f.kind}
              autocomplete="off"
              bind:value={form.survey[f.key]}
            />
          {/if}
        {/if}
        {#if f.description}<p class="text-xs text-muted">{f.description}</p>{/if}
      </div>
    {/each}
  {/if}
</fieldset>
