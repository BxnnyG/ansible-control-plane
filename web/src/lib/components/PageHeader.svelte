<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    description = '',
    badge,
    meta,
    actions,
  }: {
    title: string;
    description?: string;
    /** Inline status next to the title (for example a status pill). */
    badge?: Snippet;
    /** Rich lines under the title, for detail pages; shown after the description. */
    meta?: Snippet;
    actions?: Snippet;
  } = $props();
</script>

<header
  class="mb-5 flex flex-wrap items-end justify-between gap-x-4 gap-y-3 border-b border-border pb-4"
>
  <div class="min-w-0">
    <h1 class="flex flex-wrap items-center gap-3 text-xl font-semibold">
      {title}{#if badge}{@render badge()}{/if}
    </h1>
    {#if description}<p class="mt-0.5 max-w-2xl text-sm text-muted">{description}</p>{/if}
    {#if meta}<div class="mt-1 space-y-1 text-sm">{@render meta()}</div>{/if}
  </div>
  {#if actions}<div class="flex flex-wrap items-center gap-2">{@render actions()}</div>{/if}
</header>
