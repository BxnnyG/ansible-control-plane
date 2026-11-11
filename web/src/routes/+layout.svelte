<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { safeReturnTo } from '$lib/returnTo';
  import { session } from '$lib/session.svelte';
  import UserMenu from '$lib/components/UserMenu.svelte';
  import WorkspaceSwitcher from '$lib/components/WorkspaceSwitcher.svelte';
  import { NAV_ITEMS, groupNav, isCurrent, visibleNav } from '$lib/nav';
  import { labelTables } from '$lib/tableLabels';
  import Icon from '$lib/components/Icon.svelte';
  import BrandMark from '$lib/components/BrandMark.svelte';
  import { theme } from '$lib/theme.svelte';
  import CommandPalette from '$lib/components/CommandPalette.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Footer from '$lib/components/Footer.svelte';
  import Toasts from '$lib/components/Toasts.svelte';

  let { children } = $props();

  let paletteOpen = $state(false);
  let menuOpen = $state(false);
  let setupOffered = false;

  const bare = $derived(page.url.pathname.startsWith('/login'));

  const groups = $derived(groupNav(visibleNav(NAV_ITEMS, (p) => session.can(p))));

  onMount(() => {
    theme.init();
    void session.load();

    // Tables render as stacked cards on phones; CSS needs the column header on every cell.
    let frame = 0;
    const relabel = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => labelTables(document));
    };
    // Only table-related insertions matter; ignore everything else (e.g. streaming log lines).
    const tableNode = (n: Node) =>
      n instanceof Element &&
      (n.matches('table, thead, tbody, tr, td, th') || !!n.querySelector('table'));
    const observer = new MutationObserver((records) => {
      if (records.some((r) => [...r.addedNodes].some(tableNode))) relabel();
    });
    observer.observe(document.body, { childList: true, subtree: true });
    relabel();
    return () => {
      observer.disconnect();
      cancelAnimationFrame(frame);
    };
  });

  // Route guard: anonymous users go to /login, signed-in users leave it.
  $effect(() => {
    if (session.status === 'anonymous' && !bare) {
      const here = page.url.pathname + page.url.search;
      void goto(here === '/' ? '/login' : `/login?return_to=${encodeURIComponent(here)}`, {
        replaceState: true,
      });
    } else if (session.status === 'ready' && session.needsSetup && !setupOffered) {
      setupOffered = true; // offer the wizard once per page load, never trap the user
      void goto('/setup', { replaceState: true });
    } else if (session.status === 'ready' && bare) {
      void goto(safeReturnTo(page.url.searchParams.get('return_to'), '/'), { replaceState: true });
    }
  });

  function onwindowkeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      paletteOpen = !paletteOpen;
    } else if (e.key === 'Escape') {
      menuOpen = false;
    }
  }

  $effect(() => {
    // Close the mobile menu after navigation.
    void page.url.pathname;
    menuOpen = false;
  });
</script>

<svelte:window onkeydown={onwindowkeydown} />

{#if bare}
  <div class="flex min-h-dvh flex-col">
    <main class="flex flex-1 items-center justify-center p-4">{@render children()}</main>
    <Footer />
  </div>
{:else}
  <div class="flex min-h-dvh">
    {#if menuOpen}
      <button
        type="button"
        tabindex="-1"
        aria-hidden="true"
        class="fixed inset-0 z-20 bg-(--overlay) lg:hidden"
        onclick={() => (menuOpen = false)}
      ></button>
    {/if}
    <aside
      id="sidebar"
      class="{menuOpen
        ? 'flex'
        : 'hidden'} fixed inset-y-0 left-0 z-30 w-64 flex-col border-r border-border bg-surface shadow-2xl lg:sticky lg:top-0 lg:flex lg:h-dvh lg:w-56 lg:shrink-0 lg:shadow-none"
    >
      <a href="/" class="flex h-12 shrink-0 items-center gap-2.5 border-b border-border px-4">
        <BrandMark />
        <span class="text-sm font-semibold tracking-tight">Control Plane</span>
      </a>
      <div class="border-b border-border p-3"><WorkspaceSwitcher /></div>
      <nav aria-label="Main" class="flex-1 space-y-5 overflow-y-auto px-3 py-4">
        {#each groups as g (g.section)}
          <div>
            {#if g.section}
              <p
                class="mb-1.5 px-3 text-[0.68rem] font-semibold tracking-wider text-muted uppercase"
              >
                {g.section}
              </p>
            {/if}
            <ul class="space-y-0.5">
              {#each g.items as item (item.href)}
                {@const current = isCurrent(item, page.url.pathname)}
                <li>
                  <a
                    href={item.href}
                    aria-current={current ? 'page' : undefined}
                    class="relative flex min-h-11 items-center gap-2.5 rounded-lg px-3 py-1.5 text-sm lg:min-h-9 {current
                      ? 'bg-accent-soft font-medium text-accent'
                      : 'text-muted hover:bg-surface-2 hover:text-fg'}"
                  >
                    <Icon d={item.icon} class="h-4 w-4 shrink-0" />
                    {item.label}
                  </a>
                </li>
              {/each}
            </ul>
          </div>
        {/each}
      </nav>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <header
        class="sticky top-0 z-10 flex h-12 items-center gap-1.5 border-b border-border bg-bg/90 px-2 backdrop-blur sm:px-5 lg:gap-2"
      >
        <button
          type="button"
          class="inline-flex h-11 w-11 items-center justify-center rounded-md hover:bg-surface-2 lg:hidden"
          aria-label="Toggle navigation"
          aria-controls="sidebar"
          aria-expanded={menuOpen}
          onclick={() => (menuOpen = !menuOpen)}
        >
          <Icon d={menuOpen ? 'M6 6l12 12M18 6 6 18' : 'M4 6h16M4 12h16M4 18h16'} />
        </button>
        <a href="/" class="flex items-center gap-2 lg:hidden" aria-label="Dashboard">
          <BrandMark />
        </a>

        <div class="flex-1"></div>

        <button
          type="button"
          class="hidden min-w-56 items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1 text-sm text-muted hover:border-border-strong sm:flex"
          onclick={() => (paletteOpen = true)}
        >
          <Icon d="M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM21 21l-4.3-4.3" class="h-4 w-4" />
          Search
          <kbd class="ml-auto rounded border border-border px-1.5 text-xs">Ctrl K</kbd>
        </button>
        <button
          type="button"
          class="inline-flex h-11 w-11 items-center justify-center rounded-md hover:bg-surface-2 sm:hidden"
          aria-label="Search pages"
          onclick={() => (paletteOpen = true)}
        >
          <Icon d="M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM21 21l-4.3-4.3" />
        </button>

        <button
          type="button"
          class="inline-flex h-11 w-11 items-center justify-center rounded-md hover:bg-surface-2 lg:h-8 lg:w-8"
          aria-label="Switch to {theme.current === 'dark' ? 'light' : 'dark'} theme"
          onclick={() => theme.toggle()}
        >
          <Icon
            class="h-5 w-5 lg:h-4 lg:w-4"
            d={theme.current === 'dark'
              ? 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8zM12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5 19 19M5 19l1.5-1.5M17.5 6.5 19 5'
              : 'M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z'}
          />
        </button>

        <UserMenu />
      </header>

      <main class="mx-auto w-full max-w-[96rem] flex-1 px-4 py-5 sm:px-6 lg:px-8 lg:py-6">
        {#if session.status === 'ready'}
          <div class="rise">{@render children()}</div>
        {:else if session.status === 'error'}
          <p role="alert">
            Could not reach the server.
            <button type="button" class="underline" onclick={() => session.load()}>Retry</button>
          </p>
        {:else}
          <div role="status" class="space-y-3">
            <span class="sr-only">Loading...</span>
            <Skeleton class="h-7 w-40" />
            <Skeleton class="h-24 w-full" />
            <Skeleton class="h-24 w-full" />
          </div>
        {/if}
      </main>
      <Footer />
    </div>
  </div>
{/if}

<CommandPalette bind:open={paletteOpen} />
<Toasts />
