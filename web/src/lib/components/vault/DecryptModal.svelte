<script lang="ts">
  import { api, type VaultDecrypted } from '$lib/api';
  import { btn } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';

  let {
    ws,
    projectId,
    path,
    keyPath = '',
    onclose,
  }: {
    ws: string;
    projectId: string;
    path: string;
    keyPath?: string;
    onclose: () => void;
  } = $props();

  let dialog: HTMLDialogElement | undefined = $state();
  let result = $state<VaultDecrypted | null>(null);
  let error = $state<unknown>(null);
  let busy = $state(true);

  // The plaintext only lives in this component and is dropped on close/destroy.
  function clear() {
    result = null;
  }

  function close() {
    clear();
    dialog?.close();
    onclose();
  }

  $effect(() => {
    dialog?.showModal();
    let alive = true;
    api.vault
      .decrypt(ws, projectId, path, keyPath)
      .then((r) => {
        if (alive) result = r;
      })
      .catch((e) => {
        if (alive) error = e;
      })
      .finally(() => {
        if (alive) busy = false;
      });
    return () => {
      alive = false;
      clear();
    };
  });
</script>

<dialog
  bind:this={dialog}
  aria-labelledby="dec-h"
  class="dlg m-auto w-full max-w-2xl rounded-2xl border border-border bg-surface p-5 text-fg backdrop:bg-(--overlay)"
  oncancel={(e) => {
    e.preventDefault();
    close();
  }}
>
  <h2 id="dec-h" class="text-lg font-medium">Decrypted value</h2>
  <p class="mt-1 text-sm break-all text-muted">
    <code>{path}</code>{#if keyPath}&nbsp;<code>{keyPath}</code>{/if}
  </p>
  <p class="mt-2 text-sm text-warn">
    This action is recorded in the audit log. Do not share the content.
  </p>
  {#if busy}
    <p class="mt-3 text-muted">Decrypting...</p>
  {:else if result}
    {#if result.encoding === 'base64'}
      <p class="mt-3 text-sm text-muted">Binary content, shown base64-encoded.</p>
    {/if}
    <pre
      class="mt-3 max-h-96 overflow-auto rounded-md bg-surface-2 p-3 text-xs break-all whitespace-pre-wrap"
      data-testid="plaintext">{result.plaintext}</pre>
    <p class="mt-1 text-xs text-muted">Identity: {result.identity}</p>
  {/if}
  <ErrorBox {error} />
  <div class="mt-3 text-right"><button class={btn} onclick={close}>Close</button></div>
</dialog>
