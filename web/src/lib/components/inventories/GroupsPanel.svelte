<script lang="ts">
  import { tick } from 'svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import type { InventoryGroup, InventoryGroupInput, InventoryHost } from '$lib/api';
  import { hostCounts, visibleRows, type TreeRow } from '$lib/groupBrowse';
  import { buildGroupTree } from '$lib/groupTree';
  import { btn, btnDanger, btnPrimary, input, label, mono } from '$lib/ui';
  import Icon from '$lib/components/Icon.svelte';
  import VarsEditor from './VarsEditor.svelte';

  let {
    groups,
    hosts = [],
    canWrite,
    save,
    remove,
  }: {
    groups: InventoryGroup[];
    hosts?: InventoryHost[];
    canWrite: boolean;
    /** Creates (id null) or updates a group; the parent handles errors and reloads. */
    save: (id: string | null, g: InventoryGroupInput) => Promise<boolean>;
    remove: (g: InventoryGroup) => Promise<void>;
  } = $props();

  const CHEVRON = 'M9 6l6 6-6 6';
  const FOLDER = 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z';

  const tree = $derived(buildGroupTree(groups));
  const counts = $derived(hostCounts(groups, hosts));
  const byId = $derived(new Map(groups.map((g) => [g.id, g])));

  let filter = $state('');
  const collapsed = new SvelteSet<string>();
  let selectedId = $state<string | null>(null);
  let focusKey = $state<string | null>(null);
  let menuKey = $state<string | null>(null);
  let confirmId = $state<string | null>(null);
  let treeEl: HTMLUListElement | undefined = $state();
  let detailEl: HTMLElement | undefined = $state();

  const rows = $derived(visibleRows(tree, collapsed, filter));
  const selected = $derived(
    (selectedId ? byId.get(selectedId) : undefined) ?? rows[0]?.node.group ?? null,
  );
  const tabKey = $derived(
    rows.find((r) => r.key === focusKey)?.key ??
      rows.find((r) => r.node.group.id === selected?.id)?.key ??
      rows[0]?.key ??
      null,
  );

  const hostsOf = (gid: string) => hosts.filter((h) => h.group_ids.includes(gid));
  const childrenOf = (gid: string) =>
    groups.filter((g) => g.parent_ids.includes(gid)).sort((a, b) => a.name.localeCompare(b.name));

  function toggle(row: TreeRow) {
    if (collapsed.has(row.key)) collapsed.delete(row.key);
    else collapsed.add(row.key);
  }

  function choose(row: TreeRow) {
    selectedId = row.node.group.id;
    focusKey = row.key;
    editing = null;
    // On narrow screens the detail pane sits below the tree.
    if (window.matchMedia('(max-width: 1023px)').matches) {
      void tick().then(() => detailEl?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }));
    }
  }

  async function focusRow(key: string | null) {
    if (!key) return;
    focusKey = key;
    await tick();
    treeEl?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)?.focus();
  }

  function onkeydown(e: KeyboardEvent, row: TreeRow) {
    const i = rows.findIndex((r) => r.key === row.key);
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        void focusRow(rows[Math.min(i + 1, rows.length - 1)].key);
        break;
      case 'ArrowUp':
        e.preventDefault();
        void focusRow(rows[Math.max(i - 1, 0)].key);
        break;
      case 'Home':
        e.preventDefault();
        void focusRow(rows[0].key);
        break;
      case 'End':
        e.preventDefault();
        void focusRow(rows[rows.length - 1].key);
        break;
      case 'ArrowRight':
        e.preventDefault();
        if (row.hasChildren && !row.expanded) toggle(row);
        else if (row.expanded) void focusRow(rows[i + 1]?.key ?? null);
        break;
      case 'ArrowLeft':
        e.preventDefault();
        if (row.expanded && !filter) toggle(row);
        else void focusRow(row.parentKey);
        break;
      case 'Enter':
      case ' ':
        e.preventDefault();
        choose(row);
        break;
      case 'ContextMenu':
      case 'F10':
        if (canWrite && !row.node.repeated && (e.key === 'ContextMenu' || e.shiftKey)) {
          e.preventDefault();
          openMenu(row);
        }
        break;
    }
  }

  function openMenu(row: TreeRow) {
    confirmId = null;
    menuKey = menuKey === row.key ? null : row.key;
    selectedId = row.node.group.id;
  }

  // ---- edit flow (unchanged behaviour: create, update, delete via the parent) ----
  let editing = $state<InventoryGroup | 'new' | null>(null);
  let name = $state('');
  let parents = $state<string[]>([]);
  let vars = $state('');

  function open(g: InventoryGroup | 'new') {
    menuKey = null;
    editing = g;
    name = g === 'new' ? '' : g.name;
    parents = g === 'new' ? [] : [...g.parent_ids];
    vars = g === 'new' ? '' : g.vars_yaml;
    if (g !== 'new') selectedId = g.id;
    void tick().then(() => detailEl?.scrollIntoView({ block: 'nearest' }));
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const id = editing === 'new' || editing === null ? null : editing.id;
    if (await save(id, { name, parent_ids: parents, vars_yaml: vars })) editing = null;
  }

  async function doDelete(g: InventoryGroup) {
    menuKey = null;
    confirmId = null;
    await remove(g);
  }

  const candidates = $derived(
    groups.filter((g) => editing === 'new' || editing === null || g.id !== editing.id),
  );
</script>

<svelte:window
  onclick={(e) => {
    if (menuKey && !(e.target as Element).closest('[data-group-menu]')) menuKey = null;
  }}
  onkeydown={(e) => e.key === 'Escape' && (menuKey = null)}
/>

<section aria-labelledby="groups-h">
  <div class="mb-2 flex items-center justify-between gap-3">
    <h2 id="groups-h" class="text-sm font-semibold">
      Groups <span class="font-mono">({groups.length})</span>
    </h2>
    {#if canWrite && editing === null}
      <button class={btn} onclick={() => open('new')}>Add group</button>
    {/if}
  </div>

  {#if groups.length === 0 && editing === null}
    <p class="rounded-md border border-dashed border-border-strong p-4 text-sm text-muted">
      No groups.
    </p>
  {:else}
    <div
      class="grid overflow-hidden rounded-md border border-border bg-surface lg:grid-cols-[minmax(16rem,22rem)_1fr]"
    >
      <!-- Tree pane -->
      <div class="flex min-h-0 flex-col border-border max-lg:border-b lg:border-r">
        <div class="border-b border-border p-2">
          <input
            class={input}
            type="search"
            placeholder="Filter groups"
            aria-label="Filter groups"
            bind:value={filter}
          />
        </div>
        <ul
          bind:this={treeEl}
          role="tree"
          aria-label="Groups"
          class="max-h-72 overflow-auto py-1 lg:max-h-[30rem]"
        >
          {#each rows as row (row.key)}
            {@const g = row.node.group}
            {@const c = counts.get(g.id)}
            {@const isSel = selected?.id === g.id}
            <li
              role="treeitem"
              data-key={row.key}
              aria-level={row.depth + 1}
              aria-selected={isSel}
              aria-expanded={row.hasChildren ? row.expanded : undefined}
              tabindex={row.key === tabKey ? 0 : -1}
              class="group relative flex min-h-11 cursor-pointer items-center gap-1 pr-1 text-sm outline-offset-[-2px] lg:min-h-8 {isSel
                ? 'bg-surface-2 font-medium'
                : 'hover:bg-surface-2/60'}"
              style="padding-left: {0.5 + row.depth * 1.1}rem"
              onclick={() => choose(row)}
              onkeydown={(e) => e.target === e.currentTarget && onkeydown(e, row)}
              onfocus={() => (focusKey = row.key)}
            >
              {#if isSel}<span class="absolute inset-y-0 left-0 w-0.5 bg-accent-fill"></span>{/if}
              {#each Array.from({ length: row.depth }, (_, d) => d) as d (d)}
                <span
                  class="pointer-events-none absolute inset-y-0 w-px bg-border"
                  style="left: {0.5 + d * 1.1 + 0.55}rem"
                  aria-hidden="true"
                ></span>
              {/each}
              {#if row.hasChildren}
                <button
                  type="button"
                  tabindex="-1"
                  class="relative inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted hover:bg-border max-lg:h-8 max-lg:w-8"
                  aria-label="{row.expanded ? 'Collapse' : 'Expand'} {g.name}"
                  onclick={(e) => {
                    e.stopPropagation();
                    toggle(row);
                  }}
                >
                  <Icon
                    d={CHEVRON}
                    class="h-3.5 w-3.5 transition-transform {row.expanded ? 'rotate-90' : ''}"
                  />
                </button>
              {:else}
                <span class="inline-block h-6 w-6 shrink-0 max-lg:w-8"></span>
              {/if}
              <Icon d={FOLDER} class="relative h-4 w-4 shrink-0 text-muted" />
              <span
                class="min-w-0 flex-1 truncate font-mono text-[0.92em] {row.node.repeated
                  ? 'text-muted italic'
                  : ''}"
              >
                {g.name}
              </span>
              {#if c}
                <span
                  class="rounded border border-border px-1.5 font-mono text-[0.7rem] leading-4 text-muted"
                  title="{c.direct} direct, {c.total} including child groups">{c.total}</span
                >
              {/if}
              {#if canWrite && !row.node.repeated}
                <span class="relative" data-group-menu>
                  <button
                    type="button"
                    tabindex={isSel ? 0 : -1}
                    class="inline-flex h-6 w-6 items-center justify-center rounded text-muted hover:bg-border hover:text-fg max-lg:h-9 max-lg:w-9 lg:opacity-0 lg:group-focus-within:opacity-100 lg:group-hover:opacity-100 {menuKey ===
                    row.key
                      ? 'lg:!opacity-100'
                      : ''}"
                    aria-label="Actions for {g.name}"
                    aria-haspopup="menu"
                    aria-expanded={menuKey === row.key}
                    onclick={(e) => {
                      e.stopPropagation();
                      openMenu(row);
                    }}
                  >
                    <svg viewBox="0 0 24 24" class="h-4 w-4" fill="currentColor" aria-hidden="true">
                      <circle cx="5" cy="12" r="1.8" /><circle cx="12" cy="12" r="1.8" /><circle
                        cx="19"
                        cy="12"
                        r="1.8"
                      />
                    </svg>
                  </button>
                  {#if menuKey === row.key}
                    <div
                      role="menu"
                      class="absolute right-0 z-20 mt-1 w-40 rounded-md border border-border-strong bg-surface p-1 shadow-2xl"
                    >
                      <button
                        type="button"
                        role="menuitem"
                        class="block w-full rounded px-2.5 py-1.5 text-left text-sm hover:bg-surface-2 max-lg:min-h-11"
                        onclick={(e) => {
                          e.stopPropagation();
                          open(g);
                        }}>Edit</button
                      >
                      {#if confirmId === g.id}
                        <button
                          type="button"
                          role="menuitem"
                          class="block w-full rounded bg-bad/15 px-2.5 py-1.5 text-left text-sm font-medium text-bad max-lg:min-h-11"
                          onclick={(e) => {
                            e.stopPropagation();
                            void doDelete(g);
                          }}>Confirm delete</button
                        >
                      {:else}
                        <button
                          type="button"
                          role="menuitem"
                          class="block w-full rounded px-2.5 py-1.5 text-left text-sm text-bad hover:bg-bad/10 max-lg:min-h-11"
                          onclick={(e) => {
                            e.stopPropagation();
                            confirmId = g.id;
                          }}>Delete</button
                        >
                      {/if}
                    </div>
                  {/if}
                </span>
              {:else if canWrite}
                <span class="inline-block h-6 w-6 shrink-0 max-lg:w-9"></span>
              {/if}
            </li>
          {/each}
          {#if rows.length === 0}
            <li class="px-3 py-3 text-sm text-muted">No group matches "{filter}".</li>
          {/if}
        </ul>
      </div>

      <!-- Detail pane -->
      <div bind:this={detailEl} class="min-w-0 p-4" aria-live="polite">
        {#if editing !== null}
          <form onsubmit={submit} class="space-y-3" aria-label="Group">
            <h3 class="text-sm font-semibold">
              {editing === 'new' ? 'New group' : `Edit ${editing.name}`}
            </h3>
            <div>
              <label class={label} for="g-name">Name</label>
              <input id="g-name" class={input} bind:value={name} required />
            </div>
            {#if candidates.length > 0}
              <fieldset>
                <legend class={label}>Parent groups</legend>
                <div class="flex flex-wrap gap-x-4 gap-y-1">
                  {#each candidates as c (c.id)}
                    <label class="flex items-center gap-1 text-sm max-md:min-h-11">
                      <input type="checkbox" value={c.id} bind:group={parents} />
                      {c.name}
                    </label>
                  {/each}
                </div>
              </fieldset>
            {/if}
            <VarsEditor id="g-vars" bind:value={vars} />
            <div class="flex gap-2">
              <button class={btnPrimary}>Save group</button>
              <button type="button" class={btn} onclick={() => (editing = null)}>Cancel</button>
            </div>
          </form>
        {:else if selected}
          {@const sel = selected}
          {@const members = hostsOf(sel.id)}
          {@const kids = childrenOf(sel.id)}
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="min-w-0">
              <h3 class="truncate font-mono text-base font-semibold">{sel.name}</h3>
              <p class="text-xs text-muted">
                {#if sel.parent_ids.length > 0}
                  child of
                  <span class="font-mono"
                    >{sel.parent_ids.map((p) => byId.get(p)?.name ?? '?').join(', ')}</span
                  >
                {:else}
                  top-level group
                {/if}
              </p>
            </div>
            {#if canWrite}
              <div class="flex gap-2">
                <button class={btn} onclick={() => open(sel)}>Edit</button>
                {#if confirmId === sel.id}
                  <button class={btnDanger} onclick={() => doDelete(sel)}>Confirm delete</button>
                {:else}
                  <button class={btnDanger} onclick={() => (confirmId = sel.id)}>Delete</button>
                {/if}
              </div>
            {/if}
          </div>

          <h4 class="mt-5 mb-1.5 text-xs font-semibold tracking-wider text-muted uppercase">
            Hosts <span class="font-mono">({members.length})</span>
          </h4>
          {#if members.length === 0}
            <p class="text-sm text-muted">No hosts directly in this group.</p>
          {:else}
            <ul class="grid gap-x-4 sm:grid-cols-2 xl:grid-cols-3">
              {#each members as h (h.id)}
                <li class="truncate border-b border-border py-1 {mono}">{h.name}</li>
              {/each}
            </ul>
          {/if}

          {#if kids.length > 0}
            <h4 class="mt-5 mb-1.5 text-xs font-semibold tracking-wider text-muted uppercase">
              Child groups <span class="font-mono">({kids.length})</span>
            </h4>
            <div class="flex flex-wrap gap-1.5">
              {#each kids as k (k.id)}
                <button
                  type="button"
                  class="rounded border border-border-strong px-2 py-0.5 hover:bg-surface-2 max-lg:min-h-11 {mono}"
                  onclick={() => {
                    selectedId = k.id;
                    editing = null;
                  }}>{k.name}</button
                >
              {/each}
            </div>
          {/if}

          <h4 class="mt-5 mb-1.5 text-xs font-semibold tracking-wider text-muted uppercase">
            Variables
          </h4>
          {#if sel.vars_yaml.trim()}
            <pre
              class="max-h-72 overflow-auto rounded-md border border-border bg-bg p-3 text-xs leading-5">{sel.vars_yaml.trim()}</pre>
          {:else}
            <p class="text-sm text-muted">No variables set.</p>
          {/if}
        {/if}
      </div>
    </div>
  {/if}
</section>
