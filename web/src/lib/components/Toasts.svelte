<script lang="ts">
  import { toasts } from '$lib/toasts.svelte';
  import { toastRole } from '$lib/toasts';
  import Icon from './Icon.svelte';

  const tone = { success: 'border-ok/40', error: 'border-bad/50', info: 'border-border-strong' };
  const text = { success: 'text-ok', error: 'text-bad', info: 'text-muted' };
</script>

<!-- Stacked above the bottom edge on phones, bottom-right on larger screens. -->
<div
  class="pointer-events-none fixed inset-x-3 bottom-[max(1rem,env(safe-area-inset-bottom))] z-[60] flex flex-col items-stretch gap-2 sm:inset-x-auto sm:right-4 sm:w-96"
>
  {#each toasts.items as t (t.id)}
    <div
      role={toastRole(t.kind)}
      class="pointer-events-auto flex items-start gap-3 rounded-xl border bg-surface p-3 text-sm shadow-card {tone[
        t.kind
      ]}"
    >
      <p class="min-w-0 flex-1 break-words">
        <span class="font-medium {text[t.kind]}"
          >{t.kind === 'success' ? 'Done. ' : t.kind === 'error' ? 'Error. ' : ''}</span
        >{t.message}
      </p>
      <button
        type="button"
        class="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted hover:bg-surface-2"
        aria-label="Dismiss notification"
        onclick={() => toasts.dismiss(t.id)}
      >
        <Icon d="M6 6l12 12M18 6 6 18" class="h-4 w-4" />
      </button>
    </div>
  {/each}
</div>
