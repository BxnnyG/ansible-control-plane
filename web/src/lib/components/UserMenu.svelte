<script lang="ts">
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import { session } from '$lib/session.svelte';

  let open = $state(false);
  let busy = $state(false);

  const label = $derived(session.me?.display_name || session.me?.email || 'Account');

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

<svelte:window onkeydown={(e) => e.key === 'Escape' && (open = false)} />

<div class="relative">
  <button
    type="button"
    class="rounded-full border border-border bg-surface-2 px-3 py-1.5 text-sm"
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    {label}
  </button>
  {#if open}
    <ul
      role="menu"
      class="absolute right-0 z-40 mt-1 w-44 rounded-md border border-border bg-surface p-1 shadow-lg"
    >
      <li role="none">
        <a
          role="menuitem"
          class="block rounded px-3 py-2 text-sm hover:bg-surface-2"
          href="/settings"
          onclick={() => (open = false)}>Settings</a
        >
      </li>
      <li role="none">
        <button
          type="button"
          role="menuitem"
          disabled={busy}
          class="block w-full rounded px-3 py-2 text-left text-sm hover:bg-surface-2"
          onclick={logout}>Sign out</button
        >
      </li>
    </ul>
  {/if}
</div>
