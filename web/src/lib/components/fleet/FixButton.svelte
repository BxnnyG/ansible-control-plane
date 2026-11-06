<script lang="ts">
  import { onDestroy } from 'svelte';
  import { api, type RemediationRef, type RemediationResult, type RunStatus } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { describeRemediationError, describeTarget, fixLabel } from '$lib/remediation';
  import { isFinal } from '$lib/runs';
  import { btn, btnPrimary } from '$lib/ui';

  let {
    ws,
    alertId,
    rules,
  }: {
    ws: string;
    alertId: string;
    rules: RemediationRef[];
  } = $props();

  let pending = $state<RemediationRef | null>(null);
  let chosen = $state<RemediationRef | null>(null);
  let result = $state<RemediationResult | null>(null);
  let status = $state<RunStatus | null>(null);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let timer: ReturnType<typeof setInterval> | undefined;

  function stopPolling() {
    if (timer) clearInterval(timer);
    timer = undefined;
  }
  onDestroy(stopPolling);

  // Only a check run needs watching: the apply button appears once it is done.
  function watch(runId: string) {
    stopPolling();
    status = 'queued';
    timer = setInterval(async () => {
      try {
        const r = await api.runs.get(ws, runId);
        status = r.status;
        if (isFinal(r.status)) stopPolling();
      } catch {
        stopPolling();
      }
    }, 3000);
  }

  async function start(rule: RemediationRef, skipCheck: boolean) {
    busy = true;
    error = null;
    chosen = rule;
    try {
      result = await api.remediation.remediate(ws, alertId, rule.rule_id, skipCheck);
      pending = null;
      if (result.phase === 'check') watch(result.run.id);
      else status = null;
    } catch (e) {
      error = describeRemediationError(e) ?? describeError(e);
      pending = null;
    } finally {
      busy = false;
    }
  }

  function choose(rule: RemediationRef) {
    result = null;
    status = null;
    pending = rule;
  }
</script>

{#if rules.length > 0}
  <div class="mt-1 space-y-1">
    {#if !pending && !result}
      <div class="flex flex-wrap gap-1">
        {#each rules as r (r.rule_id)}
          <button
            class={btn}
            disabled={busy}
            title="Limit: {describeTarget(r)}; template {r.template}"
            onclick={() => choose(r)}>{fixLabel(r)}</button
          >
        {/each}
      </div>
    {/if}
    {#if pending}
      <div
        role="alertdialog"
        aria-label="Confirm fix"
        class="rounded-md border border-border p-2 text-sm"
      >
        <p>
          {pending.check_first ? 'Start a check-mode run of' : 'Run'} template
          <strong>{pending.template}</strong> on {describeTarget(pending)}?
        </p>
        <div class="mt-2 flex gap-2">
          <button class={btnPrimary} disabled={busy} onclick={() => start(pending!, false)}
            >{pending.check_first ? 'Start check' : 'Run'}</button
          >
          <button class={btn} onclick={() => (pending = null)}>Cancel</button>
        </div>
      </div>
    {/if}
    {#if result}
      <div role="status" class="rounded-md border border-border p-2 text-sm">
        <p>
          {result.phase === 'check' ? 'Check run' : 'Run'} queued on {result.hosts.length} host{result
            .hosts.length === 1
            ? ''
            : 's'}:
          <a class="text-accent underline" href="/runs/{result.run.id}">open run</a>
        </p>
        {#if result.phase === 'check'}
          {#if status && isFinal(status)}
            <p class="mt-1">Check run {status}. Review the result, then apply.</p>
            {#if chosen}
              <button class="{btnPrimary} mt-1" disabled={busy} onclick={() => start(chosen!, true)}
                >Apply now</button
              >
            {/if}
          {:else}
            <p class="mt-1 text-muted">Check run {status ?? 'queued'}...</p>
          {/if}
        {/if}
        <button class="{btn} mt-1" onclick={() => ((result = null), (status = null), stopPolling())}
          >Dismiss</button
        >
      </div>
    {/if}
    {#if error}<p role="alert" class="text-sm text-red-600 dark:text-red-400">{error}</p>{/if}
  </div>
{/if}
