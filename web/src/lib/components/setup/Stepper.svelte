<script lang="ts">
  import type { SetupStepDef } from '$lib/setup/steps';

  let { steps, current }: { steps: readonly SetupStepDef[]; current: number } = $props();
</script>

<ol class="flex flex-wrap gap-2 text-sm" aria-label="Setup progress">
  {#each steps as step, i (step.id)}
    <li
      aria-current={i === current ? 'step' : undefined}
      class="rounded-full border border-border px-3 py-1 {i === current
        ? 'bg-surface-2 font-medium text-accent'
        : 'text-muted'}"
    >
      {i + 1}. {step.title}
    </li>
  {/each}
</ol>
