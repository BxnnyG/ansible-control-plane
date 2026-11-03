<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { NAV_ITEMS } from '$lib/nav';
  import { theme } from '$lib/theme.svelte';
  import CommandPalette from '$lib/components/CommandPalette.svelte';
  import Footer from '$lib/components/Footer.svelte';

  let { children } = $props();

  let paletteOpen = $state(false);
  let menuOpen = $state(false);
  let userOpen = $state(false);

  const bare = $derived(page.url.pathname.startsWith('/login'));

  onMount(() => theme.init());

  function onwindowkeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      paletteOpen = !paletteOpen;
    } else if (e.key === 'Escape') {
      userOpen = false;
      menuOpen = false;
    }
  }

  $effect(() => {
    // Close the mobile menu after navigation.
    void page.url.pathname;
    menuOpen = false;
    userOpen = false;
  });
</script>

<svelte:window onkeydown={onwindowkeydown} />

{#if bare}
  <div class="flex min-h-screen flex-col">
    <main class="flex flex-1 items-center justify-center p-4">{@render children()}</main>
    <Footer />
  </div>
{:else}
  <div class="flex min-h-screen">
    <aside
      id="sidebar"
      class="{menuOpen
        ? 'flex'
        : 'hidden'} fixed inset-y-0 left-0 z-30 w-56 flex-col border-r border-border bg-surface md:static md:flex"
    >
      <a href="/" class="px-4 py-4 text-lg font-semibold">Control Plane</a>
      <nav aria-label="Main" class="flex-1 px-2">
        <ul class="space-y-1">
          {#each NAV_ITEMS as item (item.href)}
            {@const current = page.url.pathname.startsWith(item.href)}
            <li>
              <a
                href={item.href}
                aria-current={current ? 'page' : undefined}
                class="flex items-center gap-3 rounded-md px-3 py-2 text-sm hover:bg-surface-2 {current
                  ? 'bg-surface-2 font-medium text-accent'
                  : 'text-fg'}"
              >
                <svg
                  viewBox="0 0 24 24"
                  class="h-5 w-5"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  aria-hidden="true"><path d={item.icon} /></svg
                >
                {item.label}
              </a>
            </li>
          {/each}
        </ul>
      </nav>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <header class="flex items-center gap-2 border-b border-border bg-surface px-4 py-2 sm:gap-3">
        <button
          type="button"
          class="rounded-md p-2 hover:bg-surface-2 md:hidden"
          aria-label="Toggle navigation"
          aria-controls="sidebar"
          aria-expanded={menuOpen}
          onclick={() => (menuOpen = !menuOpen)}
        >
          <svg
            viewBox="0 0 24 24"
            class="h-5 w-5"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16" /></svg
          >
        </button>

        <button
          type="button"
          class="rounded-md border border-border px-3 py-1.5 text-sm hover:bg-surface-2"
          aria-label="Workspace switcher (placeholder)"
          disabled
        >
          Default workspace
        </button>

        <div class="flex-1"></div>

        <button
          type="button"
          class="hidden rounded-md border border-border px-3 py-1.5 text-sm text-muted hover:bg-surface-2 sm:block"
          onclick={() => (paletteOpen = true)}
        >
          Search <kbd class="ml-2 rounded bg-surface-2 px-1.5 text-xs">Ctrl K</kbd>
        </button>

        <button
          type="button"
          class="rounded-md p-2 hover:bg-surface-2"
          aria-label="Switch to {theme.current === 'dark' ? 'light' : 'dark'} theme"
          onclick={() => theme.toggle()}
        >
          <svg
            viewBox="0 0 24 24"
            class="h-5 w-5"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            {#if theme.current === 'dark'}
              <circle cx="12" cy="12" r="4" /><path
                d="M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5 19 19M5 19l1.5-1.5M17.5 6.5 19 5"
              />
            {:else}
              <path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z" />
            {/if}
          </svg>
        </button>

        <div class="relative">
          <button
            type="button"
            class="rounded-full border border-border bg-surface-2 px-3 py-1.5 text-sm"
            aria-haspopup="menu"
            aria-expanded={userOpen}
            onclick={() => (userOpen = !userOpen)}
          >
            User
          </button>
          {#if userOpen}
            <ul
              role="menu"
              class="absolute right-0 z-40 mt-1 w-40 rounded-md border border-border bg-surface p-1 shadow-lg"
            >
              <li role="none">
                <a
                  role="menuitem"
                  class="block rounded px-3 py-2 text-sm hover:bg-surface-2"
                  href="/settings">Settings</a
                >
              </li>
              <li role="none">
                <a
                  role="menuitem"
                  class="block rounded px-3 py-2 text-sm hover:bg-surface-2"
                  href="/login">Sign in</a
                >
              </li>
            </ul>
          {/if}
        </div>
      </header>

      <main class="flex-1 p-4 sm:p-6">{@render children()}</main>
      <Footer />
    </div>
  </div>
{/if}

<CommandPalette bind:open={paletteOpen} />
