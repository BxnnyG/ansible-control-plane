<script lang="ts">
  import type { InventoryGroup, InventoryHost, InventoryHostInput } from '$lib/api';
  import { btn, btnPrimary, input, label, td, th } from '$lib/ui';
  import VarsEditor from './VarsEditor.svelte';

  let {
    hosts,
    groups,
    canWrite,
    save,
    remove,
  }: {
    hosts: InventoryHost[];
    groups: InventoryGroup[];
    canWrite: boolean;
    save: (id: string | null, h: InventoryHostInput) => Promise<boolean>;
    remove: (h: InventoryHost) => Promise<void>;
  } = $props();

  const groupName = $derived(new Map(groups.map((g) => [g.id, g.name])));

  let editing = $state<InventoryHost | 'new' | null>(null);
  let name = $state('');
  let memberOf = $state<string[]>([]);
  let vars = $state('');

  function open(h: InventoryHost | 'new') {
    editing = h;
    name = h === 'new' ? '' : h.name;
    memberOf = h === 'new' ? [] : [...h.group_ids];
    vars = h === 'new' ? '' : h.vars_yaml;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const id = editing === 'new' || editing === null ? null : editing.id;
    if (await save(id, { name, group_ids: memberOf, vars_yaml: vars })) editing = null;
  }
</script>

<section aria-labelledby="hosts-h">
  <div class="mb-2 flex items-center justify-between">
    <h2 id="hosts-h" class="text-sm font-semibold">Hosts ({hosts.length})</h2>
    {#if canWrite && editing === null}
      <button class={btn} onclick={() => open('new')}>Add host</button>
    {/if}
  </div>

  {#if hosts.length === 0}
    <p class="text-sm text-muted">No hosts.</p>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead>
          <tr>
            <th class={th}>Name</th><th class={th}>Groups</th><th class={th}>Vars</th>
            <th class={th}><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each hosts as h (h.id)}
            <tr>
              <td class="{td} font-mono">{h.name}</td>
              <td class={td}>{h.group_ids.map((g) => groupName.get(g) ?? '?').join(', ') || '-'}</td
              >
              <td class={td}
                >{h.vars_yaml.trim() ? `${h.vars_yaml.trim().split('\n').length} lines` : '-'}</td
              >
              <td class="{td} text-right whitespace-nowrap">
                {#if canWrite}
                  <button
                    class="text-xs text-muted hover:text-fg hover:underline"
                    onclick={() => open(h)}
                  >
                    edit
                  </button>
                  <button class="text-xs text-bad hover:underline" onclick={() => remove(h)}>
                    delete
                  </button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if editing !== null}
    <form onsubmit={submit} class="mt-4 space-y-3 border-t border-border pt-4" aria-label="Host">
      <div>
        <label class={label} for="h-name">Name</label>
        <input
          id="h-name"
          class={input}
          bind:value={name}
          required
          placeholder="web1.example.test"
        />
      </div>
      {#if groups.length > 0}
        <fieldset>
          <legend class={label}>Groups</legend>
          <div class="flex flex-wrap gap-x-4 gap-y-1">
            {#each groups as g (g.id)}
              <label class="flex items-center gap-1 text-sm">
                <input type="checkbox" value={g.id} bind:group={memberOf} />
                {g.name}
              </label>
            {/each}
          </div>
        </fieldset>
      {/if}
      <VarsEditor id="h-vars" bind:value={vars} />
      <div class="flex gap-2">
        <button class={btnPrimary}>Save host</button>
        <button type="button" class={btn} onclick={() => (editing = null)}>Cancel</button>
      </div>
    </form>
  {/if}
</section>
