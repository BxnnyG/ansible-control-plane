<script lang="ts">
  import type { Survey } from '$lib/scan';
  import { surveyToFormFields } from '$lib/survey';
  import { input } from '$lib/ui';

  let { survey }: { survey: Survey } = $props();

  const fields = $derived(surveyToFormFields(survey.fields));
</script>

<!-- Read-only preview: every control is disabled and never submits. -->
<form class="space-y-2" onsubmit={(e) => e.preventDefault()} aria-label="Survey preview">
  <p class="text-sm font-medium">{survey.title || survey.entry_point}</p>
  {#if survey.description}<p class="text-xs text-muted">{survey.description}</p>{/if}
  {#if fields.length === 0}<p class="text-xs text-muted">No inputs.</p>{/if}
  {#each fields as f (f.key)}
    <div style="margin-left: {f.depth * 1}rem">
      {#if f.group}
        <p class="text-sm font-medium">{f.label}</p>
      {:else if f.kind === 'checkbox'}
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" disabled checked={f.value === 'true'} />
          {f.label}{f.required ? ' *' : ''}
        </label>
      {:else}
        <label class="block text-sm" for="sv-{survey.entry_point}-{f.key}">
          {f.label}{f.required ? ' *' : ''}
        </label>
        {#if f.kind === 'select'}
          <select id="sv-{survey.entry_point}-{f.key}" class={input} disabled>
            {#each f.choices as c (c)}<option selected={c === f.value}>{c}</option>{/each}
          </select>
        {:else if f.kind === 'textarea'}
          <textarea id="sv-{survey.entry_point}-{f.key}" class="{input} font-mono" rows="2" disabled
            >{f.value}</textarea
          >
        {:else}
          <input
            id="sv-{survey.entry_point}-{f.key}"
            class={input}
            type={f.kind}
            value={f.value}
            disabled
          />
        {/if}
      {/if}
      {#if f.description}<p class="text-xs text-muted">{f.description}</p>{/if}
    </div>
  {/each}
</form>
