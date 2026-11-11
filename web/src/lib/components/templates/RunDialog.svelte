<script lang="ts">
  import { onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, type RunOverrides, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { createPreviewScheduler, previewSummary, type PreviewState } from '$lib/runPreview';
  import { buildStartRequest, startFormFromOverrides } from '$lib/runStart';
  import { surveyToFormFields } from '$lib/survey';
  import { surveyFields } from '$lib/templates';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import OverrideFields from './OverrideFields.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    template,
    onclose,
    prefill = null,
    notCarried = [],
  }: {
    ws: string;
    template: Template;
    onclose: () => void;
    /** Settings of an earlier run to start from ("Run again"). */
    prefill?: RunOverrides | null;
    /** Settings of that run that could not be carried over; shown as a notice. */
    notCarried?: string[];
  } = $props();

  // svelte-ignore state_referenced_locally
  const fields = surveyToFormFields(surveyFields(template.survey));
  // svelte-ignore state_referenced_locally
  let form = $state(startFormFromOverrides(template, fields, prefill));
  let dialog: HTMLDialogElement | undefined = $state();
  // Rendered only while open: show as modal on mount (focus moves to the first field).
  $effect(() => {
    if (dialog && !dialog.open) dialog.showModal();
  });
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

  let preview = $state<PreviewState>({ kind: 'idle' });
  const scheduler = createPreviewScheduler(
    async () => {
      const built = buildStartRequest(template, fields, form);
      if (built.errors.length > 0) throw new Error(built.errors[0]);
      return api.templates.previewRun(ws, template.id, built.overrides);
    },
    (s) => (preview = s),
    describeError,
  );
  onDestroy(() => scheduler.cancel());
  // Re-run the preview whenever the limit changes (also once on open).
  let lastLimit: string | undefined;
  $effect(() => {
    const l = form.limit;
    if (l === lastLimit) return;
    lastLimit = l;
    scheduler.request();
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    const built = buildStartRequest(template, fields, form);
    errors = built.errors;
    if (errors.length > 0) return;
    busy = true;
    try {
      const run = await api.templates.start(ws, template.id, built.overrides);
      // Close first: when the caller is the run page itself (Run again) the same
      // route component is reused for the new run and would keep the dialog open.
      onclose();
      await goto(`/runs/${run.id}`);
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<dialog
  bind:this={dialog}
  aria-labelledby="run-dialog-h"
  class="dlg m-auto w-full max-w-xl rounded-2xl border border-border bg-surface p-5 text-fg backdrop:bg-(--overlay)"
  oncancel={(e) => {
    e.preventDefault();
    onclose();
  }}
  onclick={(e) => {
    if (e.target === dialog) onclose();
  }}
>
  <div>
    <h2 id="run-dialog-h" class="text-base font-semibold">Run {template.name}</h2>
    <p class="text-sm text-muted">
      <code>{template.playbook}</code>
      {#if !template.allow_limit_override && template.default_limit}
        on <code>{template.default_limit}</code>
      {/if}
    </p>
    {#if prefill}
      <p class="mt-2 text-sm text-muted" id="run-prefill-note">
        Prefilled from the earlier run.
        {#if notCarried.length > 0}
          Not carried over: {notCarried.join(', ')}.
        {/if}
      </p>
    {/if}
    <form onsubmit={submit} class="mt-4 space-y-3">
      {#if template.allow_limit_override}
        <div>
          <label class={label} for="r-limit">Limit</label>
          <input id="r-limit" class={input} bind:value={form.limit} placeholder="all hosts" />
          <p class="text-xs text-muted">The run fails if the limit matches no host.</p>
        </div>
      {/if}
      <div id="run-preview" class="text-sm" aria-live="polite">
        {#if preview.kind === 'loading'}
          <p class="text-muted">Checking hosts...</p>
        {:else if preview.kind === 'ok'}
          <p>
            <span class="font-medium">{previewSummary(preview.preview)}</span>
            {#if preview.preview.all_in_scope}<span class="text-muted">
                (all in your scope)</span
              >{/if}
          </p>
          {#if preview.preview.hosts.length > 0}
            <p class="font-mono text-xs break-words text-muted">
              {preview.preview.hosts.join(', ')}{preview.preview.truncated ? ', ...' : ''}
            </p>
          {/if}
        {:else if preview.kind === 'error'}
          <p class="text-bad">{preview.message}</p>
        {/if}
      </div>
      <OverrideFields {template} {fields} bind:form />

      {#if !hasOverrides && fields.length === 0}
        <p class="text-sm text-muted">This template runs with its stored settings.</p>
      {/if}

      {#if errors.length > 0}
        <ul role="alert" class="list-disc pl-5 text-sm text-bad">
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
</dialog>
