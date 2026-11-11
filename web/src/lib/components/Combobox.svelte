<script lang="ts">
  import {
    filterOptions,
    flatOrder,
    groupOptions,
    moveActive,
    type ComboOption,
  } from '$lib/combobox';
  import { input } from '$lib/ui';

  let {
    id,
    options,
    value = $bindable(''),
    placeholder = 'Select...',
    required = false,
    disabled = false,
  }: {
    id: string;
    options: ComboOption[];
    value: string;
    placeholder?: string;
    required?: boolean;
    disabled?: boolean;
  } = $props();

  let open = $state(false);
  let query = $state('');
  let active = $state(-1);
  let listEl = $state<HTMLElement | null>(null);

  const selected = $derived(options.find((o) => o.value === value));
  const shown = $derived(filterOptions(options, query));
  const groups = $derived(groupOptions(shown));
  const flat = $derived(flatOrder(shown));
  const listId = $derived(`${id}-list`);
  const display = $derived(open ? query : (selected?.label ?? value));

  function openList() {
    if (disabled || open) return;
    open = true;
    query = '';
    const i = flat.findIndex((o) => o.value === value);
    active = i >= 0 ? i : flat.length > 0 ? 0 : -1;
    scrollActive();
  }

  function close() {
    open = false;
    query = '';
  }

  function choose(o: ComboOption) {
    value = o.value;
    close();
  }

  function scrollActive() {
    queueMicrotask(() =>
      listEl?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' }),
    );
  }

  function onkeydown(e: KeyboardEvent) {
    if (disabled) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Home' || e.key === 'End') {
      if (!open) openList();
      else active = moveActive(active, flat.length, e.key);
      if (e.key === 'Home' || e.key === 'End') {
        if (!open) return;
      }
      e.preventDefault();
      scrollActive();
    } else if (e.key === 'Enter') {
      if (open) {
        e.preventDefault();
        if (flat[active]) choose(flat[active]);
      }
    } else if (e.key === 'Escape') {
      if (open) {
        e.preventDefault();
        e.stopPropagation();
        close();
      }
    } else if (e.key === 'Tab') {
      close();
    }
  }

  function oninput(e: Event) {
    query = (e.currentTarget as HTMLInputElement).value;
    open = true;
    active = flatOrder(filterOptions(options, query)).length > 0 ? 0 : -1;
  }
</script>

<div class="relative">
  <input
    {id}
    type="text"
    role="combobox"
    class={input}
    autocomplete="off"
    aria-expanded={open}
    aria-controls={listId}
    aria-autocomplete="list"
    aria-activedescendant={open && active >= 0 ? `${id}-opt-${active}` : undefined}
    {placeholder}
    {disabled}
    value={display}
    onfocus={openList}
    onclick={openList}
    onblur={close}
    {oninput}
    {onkeydown}
  />
  {#if required}
    <input
      class="sr-only"
      tabindex="-1"
      aria-hidden="true"
      required
      {value}
      onfocus={(e) => (e.currentTarget.previousElementSibling as HTMLElement | null)?.focus()}
    />
  {/if}
  {#if open}
    <!-- mousedown keeps focus in the input so blur does not close before the click lands -->
    <div
      bind:this={listEl}
      id={listId}
      role="listbox"
      tabindex="-1"
      class="absolute z-30 mt-1 max-h-72 w-full overflow-auto rounded-lg border border-border bg-surface py-1 shadow-card"
      onmousedown={(e) => e.preventDefault()}
    >
      {#if flat.length === 0}
        <p class="px-3 py-2 text-sm text-muted">No match.</p>
      {/if}
      {#each groups as g (g.group)}
        <div role="group" aria-label={g.group || undefined}>
          {#if g.group}
            <p
              class="px-3 pt-2 pb-1 text-[0.68rem] font-semibold tracking-wider text-muted uppercase"
            >
              {g.group}
            </p>
          {/if}
          {#each g.options as o (o.value)}
            {@const idx = flat.indexOf(o)}
            <div
              id="{id}-opt-{idx}"
              role="option"
              tabindex="-1"
              aria-selected={o.value === value}
              data-active={idx === active}
              class="cursor-pointer px-3 py-1.5 text-sm {idx === active
                ? 'bg-surface-2'
                : ''} {o.value === value ? 'font-semibold' : ''}"
              onclick={() => choose(o)}
              onkeydown={() => {}}
              onmousemove={() => (active = idx)}
            >
              {g.group && o.label.startsWith(g.group + '/')
                ? o.label.slice(g.group.length + 1)
                : o.label}
            </div>
          {/each}
        </div>
      {/each}
    </div>
  {/if}
</div>
