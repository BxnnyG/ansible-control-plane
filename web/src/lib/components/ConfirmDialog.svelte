<script lang="ts">
  import type { Snippet } from 'svelte';
  import { btn, btnDanger, btnPrimary } from '$lib/ui';

  let {
    title = 'Confirm',
    confirmLabel = 'Delete',
    danger = true,
    busy = false,
    onconfirm,
    oncancel,
    children,
  }: {
    title?: string;
    confirmLabel?: string;
    /** Danger styling for destructive actions; the safe button gets the initial focus either way. */
    danger?: boolean;
    busy?: boolean;
    onconfirm: () => void;
    oncancel: () => void;
    children: Snippet;
  } = $props();

  let dialog: HTMLDialogElement | undefined = $state();
  let cancelBtn: HTMLButtonElement | undefined = $state();

  // Rendered only while a confirmation is pending: open as modal on mount.
  $effect(() => {
    if (dialog && !dialog.open) {
      dialog.showModal();
      cancelBtn?.focus();
    }
  });
</script>

<dialog
  bind:this={dialog}
  role="alertdialog"
  aria-labelledby="confirm-h"
  class="dlg m-auto w-full max-w-md rounded-2xl border border-border bg-surface p-5 text-fg backdrop:bg-(--overlay)"
  oncancel={(e) => {
    e.preventDefault();
    oncancel();
  }}
  onclick={(e) => {
    if (e.target === dialog) oncancel();
  }}
>
  <h2 id="confirm-h" class="text-sm font-semibold">{title}</h2>
  <div class="mt-2 text-sm">{@render children()}</div>
  <div class="mt-4 flex justify-end gap-2">
    <button bind:this={cancelBtn} type="button" class={btn} onclick={oncancel}>Cancel</button>
    <button
      type="button"
      class={danger ? btnDanger : btnPrimary}
      disabled={busy}
      onclick={onconfirm}>{confirmLabel}</button
    >
  </div>
</dialog>
