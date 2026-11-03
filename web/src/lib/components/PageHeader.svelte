<script lang="ts">
  let { title, description }: { title: string; description: string } = $props();
</script>

<svelte:head><title>{title} - Ansible Control Plane</title></svelte:head>

<header class="mb-6">
  <h1 class="text-2xl font-semibold">{title}</h1>
  <p class="mt-1 text-muted">{description}</p>
</header>
<div class="rounded-lg border border-dashed border-border bg-surface p-8 text-center text-muted">
  Nothing here yet. This page is a placeholder.
</div>
