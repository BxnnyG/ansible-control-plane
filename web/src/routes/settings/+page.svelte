<script lang="ts">
  import PageHeader from '$lib/components/PageHeader.svelte';
  import NetworkTrackingPanel from '$lib/components/fleet/NetworkTrackingPanel.svelte';
  import { session } from '$lib/session.svelte';
  import { card } from '$lib/ui';
</script>

<svelte:head><title>Settings - Ansible Control Plane</title></svelte:head>

<PageHeader title="Settings" description="Workspace, access and platform configuration." />
{#if session.slug && session.can('fleet.read')}
  <section aria-labelledby="tracking-h" class="{card} max-w-3xl">
    <h2 id="tracking-h" class="mb-3 text-lg font-medium">Fleet networks</h2>
    <NetworkTrackingPanel ws={session.slug} />
  </section>
{:else}
  <div class="empty">Nothing to configure here for your role.</div>
{/if}
