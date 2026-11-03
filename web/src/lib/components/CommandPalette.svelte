<script lang="ts">
  import { goto } from '$app/navigation';
  import { NAV_ITEMS, filterNav } from '$lib/nav';

  let { open = $bindable(false) }: { open: boolean } = $props();

  let query = $state('');
  let active = $state(0);
  let dialog: HTMLDialogElement | undefined = $state();
  let input: HTMLInputElement | undefined = $state();

  const results = $derived(filterNav(NAV_ITEMS, query));

  $effect(() => {
    if (!dialog) return;
    if (open && !dialog.open) {
      query = '';
      active = 0;
      dialog.showModal();
      input?.focus();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  });

  $effect(() => {
    // Keep the highlighted index inside the filtered list.
    if (active >= results.length) active = Math.max(0, results.length - 1);
  });

  function choose(href: string) {
    open = false;
    void goto(href);
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (results.length) active = (active + 1) % results.length;
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (results.length) active = (active - 1 + results.length) % results.length;
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const item = results[active];
      if (item) choose(item.href);
    }
  }

  function onbackdrop(e: MouseEvent) {
    if (e.target === dialog) open = false;
  }
</script>

<dialog
  bind:this={dialog}
  aria-label="Command palette"
  onclose={() => (open = false)}
  onclick={onbackdrop}
  class="m-auto mt-[15vh] w-[min(32rem,calc(100vw-2rem))] rounded-xl border border-border bg-surface p-0 text-fg shadow-xl backdrop:bg-(--overlay)"
>
  <input
    bind:this={input}
    bind:value={query}
    {onkeydown}
    type="text"
    role="combobox"
    aria-expanded="true"
    aria-controls="palette-list"
    aria-activedescendant={results[active] ? `palette-opt-${active}` : undefined}
    aria-label="Search pages"
    placeholder="Go to..."
    autocomplete="off"
    class="w-full rounded-t-xl border-b border-border bg-transparent px-4 py-3 outline-none"
  />
  <ul id="palette-list" role="listbox" aria-label="Pages" class="max-h-72 overflow-auto p-2">
    {#each results as item, i (item.href)}
      <li
        id="palette-opt-{i}"
        role="option"
        aria-selected={i === active}
        class="cursor-pointer rounded-md px-3 py-2 {i === active ? 'bg-surface-2' : ''}"
        onmouseenter={() => (active = i)}
        onclick={() => choose(item.href)}
      >
        {item.label}
      </li>
    {:else}
      <li class="px-3 py-2 text-muted" role="presentation">No matches</li>
    {/each}
  </ul>
</dialog>
