<script lang="ts">
  import Skeleton from './Skeleton.svelte';

  let { rows = 4 }: { rows?: number } = $props();
</script>

<div role="status" class="space-y-3 rounded-2xl border border-border bg-surface p-4">
  <span class="sr-only">Loading...</span>
  {#each Array.from({ length: rows }, (_, i) => i) as i (i)}
    <Skeleton class="h-10 w-full" />
  {/each}
</div>
