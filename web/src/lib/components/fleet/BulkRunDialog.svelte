<script lang="ts">
  import { api, type BulkRunResult, type FleetHost, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { describeRemediationError } from '$lib/remediation';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    hosts,
    templates,
    onclose,
  }: {
    ws: string;
    hosts: FleetHost[];
    templates: Template[];
    onclose: () => void;
  } = $props();

  // Only templates that accept a limit can be used for a host selection.
  const usable = $derived(templates.filter((t) => t.allow_limit_override));
  // svelte-ignore state_referenced_locally
  let templateId = $state(templates.find((t) => t.allow_limit_override)?.id ?? '');
  let dialog: HTMLDialogElement | undefined = $state();
  // Rendered only while open: show as modal on mount.
  $effect(() => {
    if (dialog && !dialog.open) dialog.showModal();
  });
  let checkMode = $state(true);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let result = $state<BulkRunResult | null>(null);

  const template = $derived(templates.find((t) => t.id === templateId));
  const unlinked = $derived(hosts.filter((h) => !h.managed));
  const checkBlocked = $derived(
    checkMode && !!template && !template.check_mode && !template.allow_check_override,
  );

  async function start() {
    busy = true;
    error = null;
    try {
      result = await api.fleet.bulkRun(
        ws,
        hosts.map((h) => h.id),
        templateId,
        checkMode,
      );
    } catch (e) {
      error = describeRemediationError(e) ?? describeError(e);
    } finally {
      busy = false;
    }
  }
</script>

<dialog
  bind:this={dialog}
  aria-labelledby="bulk-run-h"
  class="dlg m-auto w-full max-w-xl rounded-2xl border border-border bg-surface p-5 text-fg backdrop:bg-(--overlay)"
  oncancel={(e) => {
    e.preventDefault();
    onclose();
  }}
  onclick={(e) => {
    if (e.target === dialog) onclose();
  }}
>
  <h2 id="bulk-run-h" class="mb-2 text-sm font-semibold">
    Run a template on {hosts.length} selected host(s)
  </h2>
  {#if result}
    <p role="status" class="text-sm">
      Run queued on {result.hosts.length} inventory host(s):
      <a
        class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
        href="/runs/{result.run.id}">open run</a
      >
    </p>
    {#if result.skipped?.length}
      <p class="mt-2 text-sm text-muted">Skipped:</p>
      <ul class="text-sm">
        {#each result.skipped as s (s.host_id)}
          <li>{s.name || s.host_id} - {s.reason}</li>
        {/each}
      </ul>
    {/if}
    <button class="{btn} mt-3" onclick={onclose}>Close</button>
  {:else}
    {#if usable.length === 0}
      <p class="text-sm text-muted">
        No template allows limit overrides. Enable that on a template first.
      </p>
    {:else}
      <div class="space-y-3">
        <div>
          <label class={label} for="br-tpl">Template</label>
          <select id="br-tpl" class={input} bind:value={templateId}>
            {#each usable as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
          </select>
        </div>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={checkMode} /> Check mode (dry run, changes nothing)
        </label>
        {#if checkBlocked}
          <p class="text-xs text-bad">This template does not allow check mode overrides.</p>
        {/if}
        {#if unlinked.length > 0}
          <p class="text-xs text-muted">
            {unlinked.length} selected host(s) are not in any inventory and will be skipped.
          </p>
        {/if}
        <p class="text-xs text-muted">
          The hosts must belong to the template's inventory and to your access scope; otherwise
          nothing is started.
        </p>
      </div>
    {/if}
    <FormError id="br-error" message={error} />
    <div class="mt-3 flex gap-2">
      <button class={btnPrimary} disabled={busy || !templateId || checkBlocked} onclick={start}
        >{checkMode ? 'Start check run' : 'Start run'}</button
      >
      <button class={btn} onclick={onclose}>Cancel</button>
    </div>
  {/if}
</dialog>
