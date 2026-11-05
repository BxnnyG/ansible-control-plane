<script lang="ts">
  import type { RunStatus } from '$lib/api';
  import { statusClass } from '$lib/runs';

  let { status }: { status: RunStatus } = $props();
</script>

<span class="inline-block rounded-full px-2 py-0.5 text-xs font-medium {statusClass(status)}"
  >{status}</span
>
