<script lang="ts">
  /** Tiny bar chart of recent run outcomes (oldest left). No chart library needed. */
  let { outcomes }: { outcomes: boolean[] } = $props();

  const W = 6;
  const GAP = 3;
  const ok = $derived(outcomes.filter(Boolean).length);
  const width = $derived(Math.max(1, outcomes.length) * (W + GAP) - GAP);
</script>

{#if outcomes.length > 0}
  <svg
    viewBox="0 0 {width} 28"
    class="h-7 w-auto max-w-full"
    role="img"
    aria-label="Last {outcomes.length} finished runs: {ok} succeeded, {outcomes.length - ok} failed"
  >
    {#each outcomes as good, i (i)}
      <rect
        x={i * (W + GAP)}
        y={good ? 8 : 0}
        width={W}
        height={good ? 20 : 28}
        rx="2"
        fill={good ? 'var(--ok)' : 'var(--bad)'}
        opacity={good ? 0.75 : 1}
      />
    {/each}
  </svg>
{/if}
