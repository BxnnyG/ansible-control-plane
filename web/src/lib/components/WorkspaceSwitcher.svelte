<script lang="ts">
  import { session } from '$lib/session.svelte';

  async function onchange(e: Event) {
    await session.select((e.currentTarget as HTMLSelectElement).value);
  }
</script>

{#if session.me && session.me.memberships.length > 0}
  <label class="block">
    <span class="mb-1 block px-1 text-[0.68rem] font-semibold tracking-wider text-muted uppercase">
      Workspace
    </span>
    <select
      class="min-h-11 w-full rounded-md border border-border-strong bg-surface px-2 py-1 text-sm font-medium lg:min-h-8"
      value={session.slug}
      {onchange}
    >
      {#each session.me.memberships as m (m.slug)}
        <option value={m.slug}>{m.name}</option>
      {/each}
    </select>
  </label>
{/if}
