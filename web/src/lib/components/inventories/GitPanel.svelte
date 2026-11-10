<script lang="ts">
  import { api, type Inventory, type InventoryCommitResult, type InventoryDiff } from '$lib/api';
  import { btn, btnPrimary, card, input, label } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';

  let {
    ws,
    inv,
    canWrite,
    onchanged,
  }: { ws: string; inv: Inventory; canWrite: boolean; onchanged: () => void } = $props();

  let diff = $state<InventoryDiff | null>(null);
  let committed = $state<InventoryCommitResult | null>(null);
  let message = $state('');
  let error = $state<unknown>(null);
  let busy = $state(false);

  async function preview() {
    busy = true;
    error = null;
    committed = null;
    try {
      diff = await api.inventories.diff(ws, inv.id);
    } catch (e) {
      error = e;
    } finally {
      busy = false;
    }
  }

  async function commit() {
    busy = true;
    error = null;
    try {
      committed = await api.inventories.commit(ws, inv.id, message, inv.version);
      diff = null;
      message = '';
      onchanged();
    } catch (e) {
      error = e;
    } finally {
      busy = false;
    }
  }
</script>

<section class={card} aria-labelledby="git-h">
  <h2 id="git-h" class="mb-2 text-sm font-semibold">Git write-back</h2>
  <p class="mb-3 text-sm text-muted">
    Changes are committed to branch <code>{inv.write_branch || 'default'}</code> of the project. Vault
    values are written in their encrypted form.
  </p>
  <button class={btn} onclick={preview} disabled={busy}>Preview changes</button>

  {#if diff}
    {#if !diff.changed}
      <p class="mt-3 text-sm text-muted">
        No changes against <code>{diff.base_sha.slice(0, 8)}</code>.
      </p>
    {:else}
      <p class="mt-3 text-sm">
        Files: {#each diff.paths as p, i (p)}{i > 0 ? ', ' : ''}<code>{p}</code>{/each}
      </p>
      <pre class="mt-2 max-h-96 overflow-auto rounded-md bg-surface-2 p-3 text-xs">{diff.diff}</pre>
      {#if canWrite}
        <div class="mt-3 max-w-xl">
          <label class={label} for="commit-msg">Commit message</label>
          <input
            id="commit-msg"
            class={input}
            bind:value={message}
            placeholder="Update inventory"
          />
          <button class="{btnPrimary} mt-2" onclick={commit} disabled={busy || !message.trim()}>
            Commit
          </button>
        </div>
      {/if}
    {/if}
  {/if}

  {#if committed}
    <p role="status" class="mt-3 text-sm text-ok">
      {committed.no_op
        ? 'Nothing to commit.'
        : `Committed ${committed.sha.slice(0, 8)} to ${committed.branch}${committed.branch_created ? ' (new branch)' : ''}.`}
    </p>
  {/if}
  <ErrorBox {error} />
</section>
