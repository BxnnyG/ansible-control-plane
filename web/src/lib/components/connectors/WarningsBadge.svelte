<script lang="ts">
  let { warnings }: { warnings: string[] } = $props();
  let open = $state(false);
  const id = `warn-${Math.random().toString(36).slice(2, 8)}`;
</script>

{#if warnings.length > 0}
  <div class="mt-1">
    <button
      type="button"
      class="tone-warn cursor-pointer hover:brightness-95"
      aria-expanded={open}
      aria-controls={id}
      onclick={() => (open = !open)}
    >
      {warnings.length}
      {warnings.length === 1 ? 'warning' : 'warnings'}
      <svg
        viewBox="0 0 24 24"
        class="h-3 w-3 {open ? 'rotate-180' : ''}"
        fill="none"
        stroke="currentColor"
        stroke-width="2.5"
        aria-hidden="true"><path d="M6 9l6 6 6-6" /></svg
      >
    </button>
    <ul {id} class="mt-1.5 space-y-1 text-xs text-warn" hidden={!open}>
      {#each warnings as w (w)}<li class="break-words">{w}</li>{/each}
    </ul>
  </div>
{/if}
