<script lang="ts">
  import { session } from '$lib/session.svelte';
  import { btnPrimary } from '$lib/ui';
</script>

{#if session.can('connector.write')}
  <a class={btnPrimary} href="/connectors">Add connector</a>
{/if}
