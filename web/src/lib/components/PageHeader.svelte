<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    description = '',
    actions,
  }: { title: string; description?: string; actions?: Snippet } = $props();
</script>

<header
  class="mb-5 flex flex-wrap items-end justify-between gap-x-4 gap-y-3 border-b border-border pb-4"
>
  <div class="min-w-0">
    <h1 class="text-xl font-semibold">{title}</h1>
    {#if description}<p class="mt-0.5 max-w-2xl text-sm text-muted">{description}</p>{/if}
  </div>
  {#if actions}<div class="flex flex-wrap items-center gap-2">{@render actions()}</div>{/if}
</header>
