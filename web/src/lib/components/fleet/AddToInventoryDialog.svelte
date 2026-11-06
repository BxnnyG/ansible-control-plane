<script lang="ts">
  import { api, type FleetHost, type Inventory } from '$lib/api';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';

  let {
    ws,
    host,
    inventories,
    onclose,
    ondone,
  }: {
    ws: string;
    host: FleetHost;
    inventories: Inventory[];
    onclose: () => void;
    ondone: (message: string) => void;
  } = $props();

  let dialog: HTMLDialogElement | undefined = $state();
  let inventoryId = $state('');
  let group = $state('');
  let name = $state('');
  let groups = $state<string[]>([]);
  let error = $state<unknown>(null);
  let busy = $state(false);

  const target = $derived(inventories.find((i) => i.id === inventoryId));

  $effect(() => {
    dialog?.showModal();
  });

  // Suggest the existing groups of the chosen inventory.
  $effect(() => {
    const id = inventoryId;
    groups = [];
    if (!id) return;
    let alive = true;
    api.inventories
      .groups(ws, id)
      .then((g) => {
        if (alive) groups = g.map((x) => x.name);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!target) return;
    busy = true;
    error = null;
    try {
      const r = await api.fleet.addToInventory(ws, host.id, {
        inventory_id: target.id,
        group: group.trim(),
        name: name.trim() || undefined,
      });
      const where = r.result.commit ? ` and committed to ${r.result.commit.branch}` : '';
      ondone(`Added ${r.host} to ${target.name}/${group.trim()}${where}.`);
    } catch (err) {
      error = err;
    } finally {
      busy = false;
    }
  }
</script>

<dialog
  bind:this={dialog}
  aria-labelledby="add-h"
  class="m-auto w-full max-w-lg rounded-lg border border-border bg-surface p-4 text-fg backdrop:bg-black/50"
  oncancel={(e) => {
    e.preventDefault();
    onclose();
  }}
>
  <h2 id="add-h" class="text-lg font-medium">Add {host.name || 'host'} to an inventory</h2>
  <p class="mt-1 text-sm text-muted">
    ansible_host is set to the primary address ({host.ips?.[0] ?? 'none'}). Git inventories get a
    commit on their write branch.
  </p>
  <form class="mt-3 space-y-3" onsubmit={submit}>
    <div>
      <label class={label} for="add-inv">Inventory</label>
      <select id="add-inv" class={input} required bind:value={inventoryId}>
        <option value="" disabled>Choose an inventory</option>
        {#each inventories as i (i.id)}<option value={i.id}>{i.name} ({i.mode})</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="add-group">Group</label>
      <input
        id="add-group"
        class={input}
        required
        list="add-groups"
        autocomplete="off"
        bind:value={group}
      />
      <datalist id="add-groups">
        {#each groups as g (g)}<option value={g}></option>{/each}
      </datalist>
      <p class="mt-1 text-xs text-muted">A group that does not exist yet is created.</p>
    </div>
    <div>
      <label class={label} for="add-name">Host name in the inventory</label>
      <input
        id="add-name"
        class={input}
        placeholder={host.name}
        autocomplete="off"
        bind:value={name}
      />
    </div>
    <ErrorBox {error} />
    <div class="flex justify-end gap-2">
      <button type="button" class={btn} onclick={onclose}>Cancel</button>
      <button type="submit" class={btnPrimary} disabled={busy || !inventoryId || !group.trim()}
        >{busy ? 'Adding...' : 'Add to inventory'}</button
      >
    </div>
  </form>
</dialog>
