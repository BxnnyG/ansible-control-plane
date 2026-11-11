<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, type FleetHost, type Inventory, type Project, type Template } from '$lib/api';
  import { NAV_ITEMS, visibleNav } from '$lib/nav';
  import { buildRows, type PaletteItem } from '$lib/palette';
  import { session } from '$lib/session.svelte';
  import RunDialog from '$lib/components/templates/RunDialog.svelte';
  import { btn } from '$lib/ui';

  let { open = $bindable(false) }: { open: boolean } = $props();

  /** Hosts are not part of the list cache; refetch on open when older than this. */
  const HOSTS_MAX_AGE_MS = 60_000;

  let query = $state('');
  let active = $state(0);
  let dialog: HTMLDialogElement | undefined = $state();
  let input: HTMLInputElement | undefined = $state();
  let loading = $state(false);
  let running = $state<Template | null>(null);

  let templates = $state<Template[]>([]);
  let hosts = $state<FleetHost[]>([]);
  let projects = $state<Project[]>([]);
  let inventories = $state<Inventory[]>([]);
  let loadedFor = '';
  let hostsAt = 0;

  const canRun = $derived(session.can('run.execute'));

  const pages = $derived(
    visibleNav(NAV_ITEMS, (p) => session.can(p)).map((n): PaletteItem => ({
      group: 'Pages',
      key: `page:${n.href}`,
      label: n.label,
      href: n.href,
    })),
  );
  const items = $derived<PaletteItem[]>([
    ...pages,
    ...templates.map((t): PaletteItem => ({
      group: 'Templates',
      key: `template:${t.id}`,
      label: t.name,
      detail: t.playbook,
      href: `/templates/${t.id}`,
      templateId: t.id,
    })),
    ...hosts.map((h): PaletteItem => ({
      group: 'Hosts',
      key: `host:${h.id}`,
      label: h.name,
      detail: [h.fqdn, ...(h.ips ?? [])].filter(Boolean).join(' '),
      href: `/fleet/hosts/${h.id}`,
    })),
    ...projects.map((p): PaletteItem => ({
      group: 'Projects',
      key: `project:${p.id}`,
      label: p.name,
      href: `/projects/${p.id}`,
    })),
    ...inventories.map((i): PaletteItem => ({
      group: 'Inventories',
      key: `inventory:${i.id}`,
      label: i.name,
      href: `/inventories/${i.id}`,
    })),
  ]);
  const rows = $derived(buildRows(items, query));

  /** Loads what the user may read, once per workspace; failures only leave a group empty. */
  async function loadResources(ws: string) {
    const stale = Date.now() - hostsAt > HOSTS_MAX_AGE_MS;
    if (loadedFor === ws && !stale) return;
    const first = loadedFor !== ws;
    if (first) {
      loadedFor = ws;
      templates = [];
      hosts = [];
      projects = [];
      inventories = [];
    }
    loading = first;
    const guard =
      <T,>(set: (v: T) => void) =>
      (v: T) => {
        if (session.slug === ws) set(v);
      };
    const jobs: Promise<unknown>[] = [];
    if (session.can('fleet.read')) {
      hostsAt = Date.now();
      jobs.push(api.fleet.hosts(ws).then((h) => guard((v: FleetHost[]) => (hosts = v))(h ?? [])));
    }
    if (first) {
      if (session.can('template.read')) {
        jobs.push(
          api.templates
            .list(ws, (d) => guard((v: Template[]) => (templates = v))(d ?? []))
            .then((d) => guard((v: Template[]) => (templates = v))(d ?? [])),
        );
      }
      if (session.can('project.read')) {
        jobs.push(
          api.projects
            .list(ws, (d) => guard((v: Project[]) => (projects = v))(d ?? []))
            .then((d) => guard((v: Project[]) => (projects = v))(d ?? [])),
        );
      }
      if (session.can('inventory.read')) {
        jobs.push(
          api.inventories
            .list(ws, (d) => guard((v: Inventory[]) => (inventories = v))(d ?? []))
            .then((d) => guard((v: Inventory[]) => (inventories = v))(d ?? [])),
        );
      }
    }
    await Promise.allSettled(jobs);
    loading = false;
  }

  $effect(() => {
    if (!dialog) return;
    if (open && !dialog.open) {
      query = '';
      active = 0;
      dialog.showModal();
      input?.focus();
      const ws = session.slug;
      if (ws) untrack(() => void loadResources(ws));
    } else if (!open && dialog.open) {
      dialog.close();
    }
  });

  $effect(() => {
    // Keep the highlighted index inside the result list.
    if (active >= rows.length) active = Math.max(0, rows.length - 1);
  });

  /** Enter and click: open the page. Never starts anything. */
  function choose(href: string) {
    open = false;
    void goto(href);
  }

  /** Explicit second action: opens the run dialog for a template. */
  function runTemplate(templateId: string | undefined) {
    const t = templates.find((x) => x.id === templateId);
    if (!t || !canRun) return;
    open = false;
    running = t;
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (rows.length) active = (active + 1) % rows.length;
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (rows.length) active = (active - 1 + rows.length) % rows.length;
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const item = rows[active]?.item;
      if (!item) return;
      if (e.ctrlKey || e.metaKey) runTemplate(item.templateId);
      else choose(item.href);
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
  class="m-auto mt-[15vh] w-[min(32rem,calc(100vw-2rem))] rounded-xl border border-border bg-surface p-0 text-fg shadow-2xl backdrop:bg-(--overlay)"
>
  <input
    bind:this={input}
    bind:value={query}
    {onkeydown}
    type="text"
    role="combobox"
    aria-expanded="true"
    aria-controls="palette-list"
    aria-activedescendant={rows[active] ? `palette-opt-${active}` : undefined}
    aria-label="Search pages, templates, hosts, projects and inventories"
    placeholder="Go to..."
    autocomplete="off"
    class="w-full rounded-t-xl border-b border-border bg-transparent px-4 py-3 outline-none"
  />
  <ul id="palette-list" role="listbox" aria-label="Results" class="max-h-80 overflow-auto p-2">
    {#each rows as row, i (row.item.key)}
      {#if row.first}
        <li
          role="presentation"
          class="px-3 pt-2 pb-1 text-[0.68rem] font-semibold uppercase tracking-wider text-muted"
        >
          {row.item.group}
        </li>
      {/if}
      <li
        id="palette-opt-{i}"
        role="option"
        aria-selected={i === active}
        class="flex cursor-pointer items-center gap-2 rounded-md px-3 py-2 {i === active
          ? 'bg-surface-2'
          : ''}"
        onmouseenter={() => (active = i)}
        tabindex="-1"
        onclick={() => choose(row.item.href)}
        onkeydown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            choose(row.item.href);
          }
        }}
      >
        <span class="min-w-0 grow truncate">
          {row.item.label}
          {#if row.item.detail}<span class="ml-2 text-xs text-muted">{row.item.detail}</span>{/if}
        </span>
        {#if row.item.templateId && canRun}
          <button
            type="button"
            class="{btn} shrink-0"
            tabindex="-1"
            aria-label="Run {row.item.label}"
            onclick={(e) => {
              e.stopPropagation();
              runTemplate(row.item.templateId);
            }}>Run...</button
          >
        {/if}
      </li>
    {:else}
      <li class="px-3 py-2 text-muted" role="presentation">
        {loading ? 'Loading...' : 'No matches'}
      </li>
    {/each}
  </ul>
  {#if canRun && templates.length > 0}
    <p class="border-t border-border px-4 py-2 text-xs text-muted">
      Enter opens. Ctrl+Enter or Run... on a template opens the run dialog.
    </p>
  {/if}
</dialog>

{#if running && session.slug}
  <RunDialog ws={session.slug} template={running} onclose={() => (running = null)} />
{/if}
