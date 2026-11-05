<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { buildStartRequest, initialStartForm } from '$lib/runStart';
  import { surveyToFormFields } from '$lib/survey';
  import { surveyFields } from '$lib/templates';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let { ws, template, onclose }: { ws: string; template: Template; onclose: () => void } = $props();

  // svelte-ignore state_referenced_locally
  const fields = surveyToFormFields(surveyFields(template.survey));
  // svelte-ignore state_referenced_locally
  let form = $state(initialStartForm(template, fields));
  let errors = $state<string[]>([]);
  let error = $state<string | null>(null);
  let busy = $state(false);

  const hasOverrides = $derived(
    template.allow_limit_override ||
      template.allow_tags_override ||
      template.allow_vars_override ||
      template.allow_check_override ||
      template.allow_diff_override ||
      template.allow_verbosity_override,
  );

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    const built = buildStartRequest(template, fields, form);
    errors = built.errors;
    if (errors.length > 0) return;
    busy = true;
    try {
      const run = await api.templates.start(ws, template.id, built.overrides);
      await goto(`/runs/${run.id}`);
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose()} />

<div
  class="fixed inset-0 z-40 flex items-start justify-center overflow-y-auto bg-[var(--overlay)] p-4"
>
  <div
    role="dialog"
    aria-modal="true"
    aria-labelledby="run-dialog-h"
    class="mt-8 w-full max-w-xl rounded-lg border border-border bg-surface p-5 shadow-lg"
  >
    <h2 id="run-dialog-h" class="text-lg font-semibold">Run {template.name}</h2>
    <p class="text-sm text-muted">
      <code>{template.playbook}</code>
      {#if !template.allow_limit_override && template.default_limit}
        on <code>{template.default_limit}</code>
      {/if}
    </p>
    <form onsubmit={submit} class="mt-4 space-y-3">
      {#if template.allow_limit_override}
        <div>
          <label class={label} for="r-limit">Limit</label>
          <input id="r-limit" class={input} bind:value={form.limit} placeholder="all hosts" />
          <p class="text-xs text-muted">The run fails if the limit matches no host.</p>
        </div>
      {/if}
      {#if template.allow_tags_override}
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <label class={label} for="r-tags">Tags</label>
            <input id="r-tags" class={input} bind:value={form.tags} />
          </div>
          <div>
            <label class={label} for="r-skip">Skip tags</label>
            <input id="r-skip" class={input} bind:value={form.skipTags} />
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
              <label class={label} for="r-verb">Verbosity</label>
              <input
                id="r-verb"
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

      {#if fields.length > 0}
        <fieldset class="space-y-2 rounded-md border border-border p-3">
          <legend class="px-1 text-sm font-medium">Survey</legend>
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
                    bind:value={form.survey[f.key]}
                  ></textarea>
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
        </fieldset>
      {/if}

      {#if template.allow_vars_override}
        <div>
          <label class={label} for="r-vars">Extra variables (JSON object)</label>
          <textarea id="r-vars" class="{input} font-mono" rows="3" bind:value={form.extraVarsJson}
          ></textarea>
        </div>
      {/if}

      {#if !hasOverrides && fields.length === 0}
        <p class="text-sm text-muted">This template runs with its stored settings.</p>
      {/if}

      {#if errors.length > 0}
        <ul role="alert" class="list-disc pl-5 text-sm text-red-600 dark:text-red-400">
          {#each errors as m (m)}<li>{m}</li>{/each}
        </ul>
      {/if}
      <FormError id="run-error" message={error} />
      <div class="flex gap-2">
        <button class={btnPrimary} disabled={busy}>{busy ? 'Starting...' : 'Start run'}</button>
        <button type="button" class={btn} onclick={onclose}>Cancel</button>
      </div>
    </form>
  </div>
</div>
