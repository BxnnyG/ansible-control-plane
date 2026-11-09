<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title = '',
    padded = true,
    actions,
    children,
  }: { title?: string; padded?: boolean; actions?: Snippet; children: Snippet } = $props();
</script>

<section class="overflow-hidden rounded-xl border border-border bg-surface shadow-card">
  {#if title || actions}
    <header
      class="flex min-h-12 items-center justify-between gap-3 border-b border-border px-5 py-2.5"
    >
      <h2 class="text-xs font-semibold tracking-wider text-muted uppercase">{title}</h2>
      {#if actions}<div class="flex items-center gap-2 text-sm">{@render actions()}</div>{/if}
    </header>
  {/if}
  <div class={padded ? 'p-5' : ''}>{@render children()}</div>
</section>
