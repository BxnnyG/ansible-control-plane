<script lang="ts">
  import {
    api,
    type GroupRule,
    type GroupRuleKind,
    type GroupRuleMode,
    type Inventory,
  } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { RULE_KINDS, buildParams, kindDef, paramValue } from '$lib/groupRules';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    rule = null,
    inventories,
    ondone,
    oncancel,
  }: {
    ws: string;
    rule?: GroupRule | null;
    inventories: Inventory[];
    ondone: () => void;
    oncancel: () => void;
  } = $props();

  // Initial values are captured once; the parent re-mounts the form per rule.
  // svelte-ignore state_referenced_locally
  let name = $state(rule?.name ?? '');
  // svelte-ignore state_referenced_locally
  let kind = $state<GroupRuleKind>(rule?.kind ?? 'subnet');
  // svelte-ignore state_referenced_locally
  let param = $state(rule ? paramValue(rule) : '');
  // svelte-ignore state_referenced_locally
  let inventoryId = $state(rule?.inventory_id ?? inventories[0]?.id ?? '');
  // svelte-ignore state_referenced_locally
  let group = $state(rule?.group ?? '');
  // svelte-ignore state_referenced_locally
  let mode = $state<GroupRuleMode>(rule?.mode ?? 'add-only');
  // svelte-ignore state_referenced_locally
  let enabled = $state(rule?.enabled ?? true);
  let error = $state<string | null>(null);
  let busy = $state(false);

  const def = $derived(kindDef(kind));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = null;
    try {
      const body = {
        name: name.trim(),
        kind,
        params: buildParams(kind, param),
        inventory_id: inventoryId,
        group: group.trim(),
        mode,
        enabled,
      };
      if (rule) await api.groupRules.update(ws, rule.id, body);
      else await api.groupRules.create(ws, body);
      ondone();
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="space-y-3" aria-label={rule ? 'Edit group rule' : 'New group rule'}>
  <div>
    <label class={label} for="gr-name">Name</label>
    <input id="gr-name" class={input} bind:value={name} required />
  </div>
  <div>
    <label class={label} for="gr-kind">Match</label>
    <select id="gr-kind" class={input} bind:value={kind} onchange={() => (param = '')}>
      {#each RULE_KINDS as k (k.kind)}<option value={k.kind}>{k.label}</option>{/each}
    </select>
  </div>
  {#if def}
    <div>
      <label class={label} for="gr-param">{def.paramLabel}</label>
      <input
        id="gr-param"
        class={input}
        bind:value={param}
        placeholder={def.placeholder}
        required
      />
    </div>
  {/if}
  <div>
    <label class={label} for="gr-inv">Target inventory</label>
    <select id="gr-inv" class={input} bind:value={inventoryId} required>
      {#each inventories as i (i.id)}<option value={i.id}>{i.name} ({i.mode})</option>{/each}
    </select>
  </div>
  <div>
    <label class={label} for="gr-group">Target group</label>
    <input id="gr-group" class={input} bind:value={group} required />
    <p class="mt-1 text-xs text-muted">Created on apply if it does not exist.</p>
  </div>
  <div>
    <label class={label} for="gr-mode">Mode</label>
    <select id="gr-mode" class={input} bind:value={mode}>
      <option value="add-only">Add only (never removes members)</option>
      <option value="sync">Sync (also removes members that no longer match)</option>
    </select>
  </div>
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={enabled} /> Enabled
  </label>
  <FormError id="gr-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy || !inventoryId}>{rule ? 'Save' : 'Create'}</button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
