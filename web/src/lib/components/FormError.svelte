<script lang="ts">
  import { btn } from '$lib/ui';

  let { id, message, onretry }: { id: string; message: string | null; onretry?: () => void } =
    $props();
</script>

<!-- Always rendered so screen readers announce the text when it changes. -->
<p {id} role="alert" class="min-h-5 text-sm text-bad">
  {message ?? ''}
  {#if message && onretry}
    <button type="button" class="{btn} ml-2" onclick={onretry}>Retry</button>
  {/if}
</p>
