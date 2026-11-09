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
      class="max-w-40 min-h-11 rounded-lg border border-border bg-surface px-3 py-1.5 font-medium sm:max-w-56 lg:min-h-0"
      value={session.slug}
      {onchange}
    >
      {#each session.me.memberships as m (m.slug)}
        <option value={m.slug}>{m.name}</option>
      {/each}
    </select>
  </label>
{/if}
