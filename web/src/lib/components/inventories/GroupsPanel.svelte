<script lang="ts">
  import type { InventoryGroup, InventoryGroupInput } from '$lib/api';
  import { buildGroupTree, flattenTree } from '$lib/groupTree';
  import { btn, btnPrimary, card, input, label } from '$lib/ui';
  import VarsEditor from './VarsEditor.svelte';

  let {
    groups,
    canWrite,
    save,
    remove,
  }: {
    groups: InventoryGroup[];
    canWrite: boolean;
    /** Creates (id null) or updates a group; the parent handles errors and reloads. */
    save: (id: string | null, g: InventoryGroupInput) => Promise<boolean>;
    remove: (g: InventoryGroup) => Promise<void>;
  } = $props();

  const rows = $derived(flattenTree(buildGroupTree(groups)));

  let editing = $state<InventoryGroup | 'new' | null>(null);
  let name = $state('');
  let parents = $state<string[]>([]);
  let vars = $state('');

  function open(g: InventoryGroup | 'new') {
    editing = g;
    name = g === 'new' ? '' : g.name;
    parents = g === 'new' ? [] : [...g.parent_ids];
    vars = g === 'new' ? '' : g.vars_yaml;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const id = editing === 'new' || editing === null ? null : editing.id;
    if (await save(id, { name, parent_ids: parents, vars_yaml: vars })) editing = null;
  }

  const candidates = $derived(
    groups.filter((g) => editing === 'new' || editing === null || g.id !== editing.id),
  );
</script>

<section class={card} aria-labelledby="groups-h">
  <div class="mb-2 flex items-center justify-between">
    <h2 id="groups-h" class="text-lg font-medium">Groups ({groups.length})</h2>
    {#if canWrite && editing === null}
      <button class={btn} onclick={() => open('new')}>Add group</button>
    {/if}
  </div>

  {#if rows.length === 0}
    <p class="text-sm text-muted">No groups.</p>
  {:else}
    <ul class="text-sm">
      {#each rows as { node, depth }, i (i)}
        <li class="flex items-center gap-2 py-0.5" style="padding-left: {depth * 1.25}rem">
          <span class={node.repeated ? 'text-muted italic' : ''}>{node.group.name}</span>
          {#if node.parentNames.length > 1}
            <span class="text-xs text-muted">
              {node.repeated ? 'also under' : 'parents'}: {node.parentNames.join(', ')}
            </span>
          {/if}
          {#if canWrite && !node.repeated}
            <button class="text-xs text-accent hover:underline" onclick={() => open(node.group)}>
              edit
            </button>
            <button
              class="text-xs text-red-600 hover:underline dark:text-red-400"
              onclick={() => remove(node.group)}
            >
              delete
            </button>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  {#if editing !== null}
    <form onsubmit={submit} class="mt-4 space-y-3 border-t border-border pt-4" aria-label="Group">
      <div>
        <label class={label} for="g-name">Name</label>
        <input id="g-name" class={input} bind:value={name} required />
      </div>
      {#if candidates.length > 0}
        <fieldset>
          <legend class={label}>Parent groups</legend>
          <div class="flex flex-wrap gap-x-4 gap-y-1">
            {#each candidates as c (c.id)}
              <label class="flex items-center gap-1 text-sm">
                <input type="checkbox" value={c.id} bind:group={parents} />
                {c.name}
              </label>
            {/each}
          </div>
        </fieldset>
      {/if}
      <VarsEditor id="g-vars" bind:value={vars} />
      <div class="flex gap-2">
        <button class={btnPrimary}>Save group</button>
        <button type="button" class={btn} onclick={() => (editing = null)}>Cancel</button>
      </div>
    </form>
  {/if}
</section>
