<script lang="ts">
  import { session } from '$lib/session.svelte';

  async function onchange(e: Event) {
    await session.select((e.currentTarget as HTMLSelectElement).value);
  }
</script>

{#if session.me && session.me.memberships.length > 0}
  <label class="flex items-center gap-2 text-sm">
    <span class="sr-only">Workspace</span>
    <select
      class="rounded-md border border-border bg-surface px-3 py-1.5"
      value={session.slug}
      {onchange}
    >
      {#each session.me.memberships as m (m.slug)}
        <option value={m.slug}>{m.name}</option>
      {/each}
    </select>
  </label>
{/if}
