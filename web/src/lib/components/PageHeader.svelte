<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    description = '',
    actions,
  }: { title: string; description?: string; actions?: Snippet } = $props();
</script>

<header class="mb-6 flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
  <div class="min-w-0">
    <h1 class="text-2xl font-semibold sm:text-3xl">{title}</h1>
    {#if description}<p class="mt-1 max-w-2xl text-muted">{description}</p>{/if}
  </div>
  {#if actions}<div class="flex flex-wrap items-center gap-2">{@render actions()}</div>{/if}
</header>
