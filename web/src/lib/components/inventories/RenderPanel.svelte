<script lang="ts">
  import { api } from '$lib/api';
  import { btn, card } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';

  let { ws, id }: { ws: string; id: string } = $props();

  let text = $state<string | null>(null);
  let error = $state<unknown>(null);
  let busy = $state(false);

  async function render() {
    busy = true;
    error = null;
    try {
      // Vault values come back masked as "<vault>".
      text = JSON.stringify(await api.inventories.render(ws, id), null, 2);
    } catch (e) {
      error = e;
    } finally {
      busy = false;
    }
  }
</script>

<section class={card} aria-labelledby="render-h">
  <h2 id="render-h" class="mb-2 text-lg font-medium">Rendered preview</h2>
  <button class={btn} onclick={render} disabled={busy}>
    {text === null ? 'Render' : 'Refresh'}
  </button>
  {#if text !== null}
    <pre class="mt-3 max-h-96 overflow-auto rounded-md bg-surface-2 p-3 text-xs">{text}</pre>
  {/if}
  <ErrorBox {error} />
</section>
