<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title = '',
    padded = true,
    actions,
    children,
  }: { title?: string; padded?: boolean; actions?: Snippet; children: Snippet } = $props();
</script>

<section class="rounded-2xl border border-border bg-surface shadow-card">
  {#if title || actions}
    <header
      class="flex items-center justify-between gap-3 border-b border-border px-4 py-3 sm:px-5"
    >
      <h2 class="text-base font-semibold">{title}</h2>
      {#if actions}<div class="flex items-center gap-2">{@render actions()}</div>{/if}
    </header>
  {/if}
  <div class={padded ? 'p-4 sm:p-5' : ''}>{@render children()}</div>
</section>
