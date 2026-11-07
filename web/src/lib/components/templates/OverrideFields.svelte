<script lang="ts">
  import type { Template } from '$lib/api';
  import type { StartForm } from '$lib/runStart';
  import type { FormField } from '$lib/survey';
  import { input, label } from '$lib/ui';
  import SurveyInputs from './SurveyInputs.svelte';

  /** Override inputs shared by the run dialog and the schedule form (limit excluded). */
  let {
    template,
    fields,
    form = $bindable(),
    idPrefix = 'r',
  }: { template: Template; fields: FormField[]; form: StartForm; idPrefix?: string } = $props();
</script>

{#if template.allow_tags_override}
  <div class="grid gap-3 sm:grid-cols-2">
    <div>
      <label class={label} for="{idPrefix}-tags">Tags</label>
      <input id="{idPrefix}-tags" class={input} bind:value={form.tags} />
    </div>
    <div>
      <label class={label} for="{idPrefix}-skip">Skip tags</label>
      <input id="{idPrefix}-skip" class={input} bind:value={form.skipTags} />
    </div>
  </div>
{/if}
{#if template.allow_check_override || template.allow_diff_override || template.allow_verbosity_override}
  <div class="flex flex-wrap items-end gap-4">
    {#if template.allow_check_override}
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={form.check} /> Check mode
      </label>
    {/if}
    {#if template.allow_diff_override}
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={form.diff} /> Diff mode
      </label>
    {/if}
    {#if template.allow_verbosity_override}
      <div class="w-32">
        <label class={label} for="{idPrefix}-verb">Verbosity</label>
        <input
          id="{idPrefix}-verb"
          type="number"
          min="0"
          max="5"
          class={input}
          bind:value={form.verbosity}
        />
      </div>
    {/if}
  </div>
{/if}
{#if fields.length > 0 || form.surveyRaw !== null}
  <SurveyInputs {fields} bind:form />
{/if}
{#if template.allow_vars_override}
  <div>
    <label class={label} for="{idPrefix}-vars">Extra variables (JSON object)</label>
    <textarea
      id="{idPrefix}-vars"
      class="{input} font-mono"
      rows="3"
      bind:value={form.extraVarsJson}
    ></textarea>
  </div>
{/if}
