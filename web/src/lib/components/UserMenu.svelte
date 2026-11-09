<script lang="ts">
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import { session } from '$lib/session.svelte';

  let open = $state(false);
  let busy = $state(false);
  let root: HTMLElement | undefined = $state();

  const label = $derived(session.me?.display_name || session.me?.email || 'Account');
  const initial = $derived(label.trim().charAt(0).toUpperCase() || '?');

  async function logout() {
    busy = true;
    let redirect = '/login';
    try {
      redirect = (await api.logout()).redirect;
    } catch {
      // The session may already be gone; continue to the login page.
    }
    session.reset();
    if (/^https?:\/\//.test(redirect)) {
      window.location.assign(redirect); // IdP end-session endpoint
    } else {
      await goto(redirect);
    }
    busy = false;
  }
</script>

<svelte:window
  onkeydown={(e) => e.key === 'Escape' && (open = false)}
  onclick={(e) => open && !root?.contains(e.target as Node) && (open = false)}
/>

<div class="relative" bind:this={root}>
  <button
    type="button"
    class="flex h-11 items-center gap-2 rounded-md border border-border py-1 pr-1 pl-1 text-sm hover:bg-surface-2 sm:pr-2.5 lg:h-8"
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <span
      class="bg-brand inline-flex h-8 w-8 items-center justify-center rounded text-xs font-semibold lg:h-6 lg:w-6"
      aria-hidden="true">{initial}</span
    >
    <span class="hidden max-w-40 truncate sm:inline">{label}</span>
    <span class="sr-only sm:hidden">{label}</span>
  </button>
  {#if open}
    <ul
      role="menu"
      class="absolute right-0 z-40 mt-2 w-48 rounded-xl border border-border-strong bg-surface p-1 shadow-2xl"
    >
      <li role="none">
        <a
          role="menuitem"
          class="block rounded-lg px-3 py-2.5 max-lg:min-h-11 lg:py-1.5 text-sm hover:bg-surface-2"
          href="/settings"
          onclick={() => (open = false)}>Settings</a
        >
      </li>
      <li role="none">
        <button
          type="button"
          role="menuitem"
          disabled={busy}
          class="block w-full rounded-lg px-3 py-2.5 max-lg:min-h-11 lg:py-1.5 text-left text-sm hover:bg-surface-2"
          onclick={logout}>Sign out</button
        >
      </li>
    </ul>
  {/if}
</div>
