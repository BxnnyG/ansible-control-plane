<script lang="ts">
  import { api, type ReachabilityTarget } from '$lib/api';
  import { describeError } from '$lib/errors';
  import {
    draftError,
    draftFrom,
    draftToInput,
    emptyDraft,
    targetSummary,
    type TargetDraft,
  } from '$lib/reachability';
  import { btn, btnDanger, btnPrimary, input, label } from '$lib/ui';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    targets,
    canWrite,
    onchange,
  }: { ws: string; targets: ReachabilityTarget[]; canWrite: boolean; onchange: () => void } =
    $props();

  let editing = $state<string | null>(null); // target id, or 'new'
  let draft = $state<TargetDraft>(emptyDraft());
  let busy = $state(false);
  let error = $state<string | null>(null);
  let removing = $state<ReachabilityTarget | null>(null);

  function startNew() {
    draft = emptyDraft();
    editing = 'new';
    error = null;
  }

  function startEdit(t: ReachabilityTarget) {
    draft = draftFrom(t);
    editing = t.id;
    error = null;
  }

  async function save(e: Event) {
    e.preventDefault();
    const msg = draftError(draft);
    if (msg) {
      error = msg;
      return;
    }
    busy = true;
    error = null;
    try {
      if (editing === 'new') await api.reachability.addTarget(ws, draftToInput(draft));
      else if (editing) await api.reachability.updateTarget(ws, editing, draftToInput(draft));
      editing = null;
      onchange();
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }

  async function confirmRemove() {
    if (!removing) return;
    busy = true;
    try {
      await api.reachability.removeTarget(ws, removing.id);
      removing = null;
      onchange();
    } catch (err) {
      error = describeError(err);
      removing = null;
    } finally {
      busy = false;
    }
  }
</script>

<div class="space-y-4 p-5">
  {#if targets.length === 0}
    <p class="text-sm text-muted">No targets yet.</p>
  {:else}
    <ul class="divide-y divide-border text-sm">
      {#each targets as t (t.id)}
        <li class="flex flex-wrap items-center justify-between gap-2 py-1.5">
          <span class="min-w-0">
            <span class="font-medium">{t.name}</span>
            <span class="font-mono text-xs text-muted">{targetSummary(t)}</span>
            {#if !t.enabled}<span class="tone-neutral ml-1">disabled</span>{/if}
          </span>
          {#if canWrite}
            <span class="flex gap-2">
              <button class={btn} onclick={() => startEdit(t)}>Edit</button>
              <button class={btnDanger} onclick={() => (removing = t)}>Delete</button>
            </span>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
  <FormError id="reach-targets-error" message={error} />
  {#if canWrite && editing === null}
    <button class={btnPrimary} onclick={startNew}>Add target</button>
  {/if}
  {#if editing !== null}
    <form class="grid gap-3 md:grid-cols-2" onsubmit={save}>
      <div>
        <label class={label} for="rt-name">Name</label>
        <input id="rt-name" class={input} bind:value={draft.name} />
      </div>
      <div>
        <label class={label} for="rt-check">Check</label>
        <select id="rt-check" class={input} bind:value={draft.check}>
          <option value="icmp">ICMP ping</option>
          <option value="tcp">TCP connect</option>
          <option value="dns">DNS lookup</option>
        </select>
      </div>
      <div>
        <label class={label} for="rt-addr"
          >Address{draft.check === 'dns' ? ' (informational)' : ''}</label
        >
        <input id="rt-addr" class={input} bind:value={draft.address} />
      </div>
      {#if draft.check === 'tcp'}
        <div>
          <label class={label} for="rt-port">Port</label>
          <input
            id="rt-port"
            class={input}
            type="number"
            min="1"
            max="65535"
            bind:value={draft.port}
          />
        </div>
      {/if}
      {#if draft.check === 'dns'}
        <div>
          <label class={label} for="rt-dns">Name to resolve</label>
          <input id="rt-dns" class={input} bind:value={draft.dns_name} />
        </div>
      {/if}
      <label class="flex min-h-11 items-center gap-2 text-sm md:col-span-2">
        <input type="checkbox" bind:checked={draft.enabled} /> Enabled
      </label>
      <div class="flex gap-2 md:col-span-2">
        <button class={btnPrimary} type="submit" disabled={busy}>Save target</button>
        <button class={btn} type="button" onclick={() => (editing = null)}>Cancel</button>
      </div>
    </form>
  {/if}
</div>

{#if removing}
  <ConfirmDialog
    title="Delete target"
    {busy}
    onconfirm={confirmRemove}
    oncancel={() => (removing = null)}
  >
    Delete target {removing.name} and its results?
  </ConfirmDialog>
{/if}
