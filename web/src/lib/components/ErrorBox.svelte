<script lang="ts">
  import { ApiError } from '$lib/api';
  import { describeError } from '$lib/errors';

  let { error }: { error: unknown } = $props();

  const paths = $derived(error instanceof ApiError ? error.paths : []);
</script>

<!-- Always rendered so screen readers announce changes. -->
<div role="alert" class="min-h-5 text-sm text-bad">
  {#if error}
    <p>{describeError(error)}</p>
    {#if paths.length > 0}
      <p class="mt-1 font-medium">Conflicting paths:</p>
      <ul class="list-inside list-disc">
        {#each paths as p (p)}<li><code>{p}</code></li>{/each}
      </ul>
    {/if}
  {/if}
</div>
