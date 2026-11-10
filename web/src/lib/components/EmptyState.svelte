<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    hint = '',
    children,
  }: { title: string; hint?: string; children?: Snippet } = $props();
</script>

<div class="empty">
  <p class="font-medium text-fg">{title}</p>
  {#if hint}<p class="mt-1 text-sm">{hint}</p>{/if}
  {#if children}<div class="mt-4 flex justify-center gap-2">{@render children()}</div>{/if}
</div>
