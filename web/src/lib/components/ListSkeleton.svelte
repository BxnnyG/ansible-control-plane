<script lang="ts">
  import Skeleton from './Skeleton.svelte';

  /** Table-shaped placeholder shown while a list loads. */
  let { rows = 5, cols = 4 }: { rows?: number; cols?: number } = $props();

  const rowIdx = $derived(Array.from({ length: rows }, (_, i) => i));
  const colIdx = $derived(Array.from({ length: cols }, (_, i) => i));
</script>

<div class="tbl-wrap" role="status" aria-busy="true" aria-label="Loading">
  <div class="divide-y divide-border">
    <div class="flex gap-4 bg-surface-2 px-3 py-3">
      {#each colIdx as c (c)}<Skeleton class="h-3 flex-1 opacity-60" />{/each}
    </div>
    {#each rowIdx as r (r)}
      <div class="flex gap-4 px-3 py-3.5">
        {#each colIdx as c (c)}
          <Skeleton class="h-4 flex-1 {(r + c) % 3 === 0 ? 'max-w-[70%]' : ''}" />
        {/each}
      </div>
    {/each}
  </div>
  <span class="sr-only">Loading...</span>
</div>
