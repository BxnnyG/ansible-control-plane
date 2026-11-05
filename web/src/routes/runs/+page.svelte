<script lang="ts">
  import { page } from '$app/state';
  import { api, runStreamUrl, type Run, type RunFilter, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { parseJsonEvent } from '$lib/runlog';
  import {
    applyRunUpdate,
    buildRunQuery,
    formatDuration,
    isUuid,
    RUN_STATUSES,
    RUN_TRIGGERS,
  } from '$lib/runs';
  import { session } from '$lib/session.svelte';
  import { btn, btnPrimary, fmtDate, input, label, td, th } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import StatusBadge from '$lib/components/runs/StatusBadge.svelte';

  const PAGE_SIZE = 25;
  const canRead = $derived(session.can('run.read'));

  let templates = $state<Template[]>([]);
  let items = $state<Run[]>([]);
  let nextCursor = $state('');
  let loading = $state(true);
  let error = $state<string | null>(null);
  let now = $state(Date.now());

  // Form state; `applied` is what the list currently shows.
  let fTemplate = $state(page.url.searchParams.get('template') ?? '');
  let fStatus = $state(page.url.searchParams.get('status') ?? '');
  let fTrigger = $state(page.url.searchParams.get('trigger') ?? '');
  let fUser = $state(page.url.searchParams.get('user') ?? '');
  let fHost = $state(page.url.searchParams.get('host') ?? '');
  let applied = $state<RunFilter>({});

  const userInvalid = $derived(fUser.trim() !== '' && !isUuid(fUser));

  function formFilter(): RunFilter {
    return {
      template: fTemplate,
      status: fStatus,
      trigger: fTrigger,
      user: fUser.trim(),
      host: fHost.trim(),
    };
  }

  async function load(ws: string, f: RunFilter, more = false) {
    loading = true;
    error = null;
    try {
      const res = await api.runs.list(
        ws,
        buildRunQuery({ ...f, limit: PAGE_SIZE, cursor: more ? nextCursor : '' }),
      );
      items = more ? [...items, ...(res.items ?? [])] : (res.items ?? []);
      nextCursor = res.next_cursor;
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  function apply(e?: SubmitEvent) {
    e?.preventDefault();
    if (userInvalid || !session.slug) return;
    applied = formFilter();
    const q = buildRunQuery(applied);
    history.replaceState(history.state, '', q ? `?${q}` : location.pathname);
    void load(session.slug, applied);
  }

  function reset() {
    fTemplate = fStatus = fTrigger = fUser = fHost = '';
    apply();
  }

  $effect(() => {
    const ws = session.slug;
    if (!ws || !canRead) {
      loading = false;
      return;
    }
    if (session.can('template.read')) {
      api.templates
        .list(ws)
        .then((t) => (templates = t ?? []))
        .catch(() => {});
    }
    applied = formFilter();
    void load(ws, applied);

    // Live status updates; the stream has no snapshot, so reload after a reconnect.
    let opened = false;
    const es = new EventSource(runStreamUrl(ws, 'events'), { withCredentials: true });
    es.onopen = () => {
      if (opened) void load(ws, applied);
      opened = true;
    };
    es.addEventListener('run', (e) => {
      const run = parseJsonEvent<Run>((e as MessageEvent<string>).data);
      if (run) items = applyRunUpdate(items, run, applied);
    });
    return () => es.close();
  });

  // Clock for durations of running runs.
  $effect(() => {
    if (!items.some((r) => r.status === 'running')) return;
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });
</script>

<svelte:head><title>Runs - Ansible Control Plane</title></svelte:head>

<header class="mb-6">
  <h1 class="text-2xl font-semibold">Runs</h1>
  <p class="mt-1 text-muted">History and live output of playbook runs.</p>
</header>

{#if !canRead}
  <p class="text-muted">You do not have permission to view runs in this workspace.</p>
{:else}
  <form onsubmit={apply} class="mb-4 flex flex-wrap items-end gap-3" aria-label="Run filters">
    <div>
      <label class={label} for="f-template">Template</label>
      <select id="f-template" class={input} bind:value={fTemplate}>
        <option value="">All</option>
        {#each templates as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="f-status">Status</label>
      <select id="f-status" class={input} bind:value={fStatus}>
        <option value="">All</option>
        {#each RUN_STATUSES as s (s)}<option value={s}>{s}</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="f-trigger">Trigger</label>
      <select id="f-trigger" class={input} bind:value={fTrigger}>
        <option value="">All</option>
        {#each RUN_TRIGGERS as t (t)}<option value={t}>{t}</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="f-user">User id</label>
      <div class="flex gap-1">
        <input
          id="f-user"
          class={input}
          bind:value={fUser}
          placeholder="uuid"
          aria-invalid={userInvalid}
        />
        {#if session.me}
          <button type="button" class={btn} onclick={() => (fUser = session.me?.id ?? '')}
            >Me</button
          >
        {/if}
      </div>
    </div>
    <div>
      <label class={label} for="f-host">Host</label>
      <input id="f-host" class={input} bind:value={fHost} placeholder="exact name" />
    </div>
    <button class={btnPrimary} disabled={userInvalid}>Filter</button>
    <button type="button" class={btn} onclick={reset}>Reset</button>
  </form>
  {#if userInvalid}<p class="mb-2 text-xs text-red-600 dark:text-red-400">
      The user filter must be a UUID.
    </p>{/if}

  <FormError id="runs-error" message={error} />

  {#if loading && items.length === 0}
    <p class="text-muted">Loading...</p>
  {:else if items.length === 0}
    <div class="rounded-lg border border-dashed border-border p-8 text-center text-muted">
      No runs found.
    </div>
  {:else}
    <div class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Status</th><th class={th}>Template</th><th class={th}>Trigger</th>
            <th class={th}>Hosts</th><th class={th}>Created</th><th class={th}>Duration</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as r (r.id)}
            <tr>
              <td class={td}><a href="/runs/{r.id}"><StatusBadge status={r.status} /></a></td>
              <td class={td}>
                <a class="text-accent hover:underline" href="/runs/{r.id}">{r.template_name}</a>
                <div class="font-mono text-xs text-muted">{r.params.playbook}</div>
              </td>
              <td class={td}>
                {r.trigger}
                {#if r.triggered_by_name}<div class="text-xs text-muted">
                    {r.triggered_by_name}
                  </div>{/if}
              </td>
              <td class={td}>
                {(r.params.hosts ?? []).length}
                {#if r.hosts_failed + r.hosts_unreachable > 0}
                  <span class="text-xs text-red-600 dark:text-red-400"
                    >({r.hosts_failed + r.hosts_unreachable} failed)</span
                  >
                {/if}
              </td>
              <td class={td}>{fmtDate(r.created_at)}</td>
              <td class={td}>{formatDuration(r.started_at, r.finished_at, now)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if nextCursor}
      <div class="mt-3">
        <button
          class={btn}
          disabled={loading}
          onclick={() => session.slug && load(session.slug, applied, true)}
        >
          Load more
        </button>
      </div>
    {/if}
  {/if}
{/if}
