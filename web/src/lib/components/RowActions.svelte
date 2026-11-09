<script lang="ts">
  import type { Snippet } from 'svelte';

  /**
   * Row action buttons. Inline on large screens; below 1024px they collapse into a "..." menu.
   * The buttons are rendered once and only repositioned by CSS.
   */
  let { children, label = 'Row actions' }: { children: Snippet; label?: string } = $props();

  let open = $state(false);
  let root: HTMLElement | undefined = $state();

  function onwindowclick(e: MouseEvent) {
    if (open && root && !root.contains(e.target as Node)) open = false;
  }
</script>

<svelte:window onclick={onwindowclick} onkeydown={(e) => e.key === 'Escape' && (open = false)} />

<div bind:this={root} class="relative inline-block lg:block">
  <button
    type="button"
    class="inline-flex min-h-11 min-w-11 items-center justify-center rounded-lg border border-border bg-surface hover:bg-surface-2 lg:hidden"
    aria-label={label}
    aria-haspopup="true"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <svg viewBox="0 0 24 24" class="h-5 w-5" fill="currentColor" aria-hidden="true">
      <circle cx="5" cy="12" r="1.8" /><circle cx="12" cy="12" r="1.8" /><circle
        cx="19"
        cy="12"
        r="1.8"
      />
    </svg>
  </button>
  <div
    class="{open
      ? 'flex'
      : 'hidden'} absolute right-0 z-20 mt-1 min-w-44 flex-col gap-1 rounded-xl border border-border bg-surface p-2 shadow-xl max-lg:[&>button]:w-full max-lg:[&>button]:justify-start lg:static lg:mt-0 lg:flex lg:min-w-0 lg:flex-row lg:flex-wrap lg:justify-end lg:gap-1.5 lg:border-0 lg:bg-transparent lg:p-0 lg:shadow-none"
    role="group"
    aria-label={label}
  >
    {@render children()}
  </div>
</div>
