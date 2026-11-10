<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { page } from '$app/state';
  import { api, runStreamUrl, type Run, type RunFilter, type Template } from '$lib/api';
  import { prepareRunAgain, type RunAgain } from '$lib/runAgain';
  import RunDialog from '$lib/components/templates/RunDialog.svelte';
  import { describeError } from '$lib/errors';
  import { parseJsonEvent } from '$lib/runlog';
  import {
    applyRunUpdate,
    buildRunQuery,
    canCancelRun,
    collectUsers,
    formatDuration,
    userOptions,
    RUN_STATUSES,
    RUN_TRIGGERS,
  } from '$lib/runs';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, fmtDate, input, label, td, th } from '$lib/ui';
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

  // There is no endpoint that lists users: the choices come from the runs loaded so far.
  let knownUsers = $state<Record<string, string>>({});
  const userChoices = $derived(userOptions(knownUsers, session.me ?? undefined, fUser));
  const canCancel = $derived(session.can('run.cancel'));
  const canRun = $derived(session.can('run.execute'));
  let again = $state<RunAgain | null>(null);
  let rowBusy = $state<string | null>(null);

  function formFilter(): RunFilter {
    return {
      template: fTemplate,
      status: fStatus,
      trigger: fTrigger,
      user: fUser.trim(),
      host: fHost.trim(),
    };
  }

  let loadSeq = 0;
  async function load(ws: string, f: RunFilter, more = false) {
    const seq = ++loadSeq;
    loading = true;
    error = null;
    try {
      const res = await api.runs.list(
        ws,
        buildRunQuery({ ...f, limit: PAGE_SIZE, cursor: more ? nextCursor : '' }),
      );
      if (seq !== loadSeq) return; // a newer request superseded this one
      items = more ? [...items, ...(res.items ?? [])] : (res.items ?? []);
      nextCursor = res.next_cursor;
      knownUsers = collectUsers(knownUsers, res.items ?? []);
    } catch (e) {
      if (seq === loadSeq) error = describeError(e);
    } finally {
      if (seq === loadSeq) loading = false;
    }
  }

  /** Applies the form as filter, updates the URL and reloads; no-op when nothing changed. */
  function apply(e?: SubmitEvent) {
    e?.preventDefault();
    clearTimeout(hostTimer);
    if (!session.slug) return;
    const next = formFilter();
    if (buildRunQuery(next) === buildRunQuery(applied)) return;
    applied = next;
    const q = buildRunQuery(applied);
    history.replaceState(history.state, '', q ? `?${q}` : location.pathname);
    void load(session.slug, applied);
  }

  // Selects apply immediately, the host text after a short pause.
  let hostTimer: ReturnType<typeof setTimeout> | undefined;
  function hostInput() {
    clearTimeout(hostTimer);
    hostTimer = setTimeout(() => apply(), 300);
  }
  onDestroy(() => clearTimeout(hostTimer));
  const selectKey = $derived([fTemplate, fStatus, fTrigger, fUser].join('|'));
  let firstKey = true;
  $effect(() => {
    void selectKey;
    if (firstKey) {
      firstKey = false; // the initial load is done by the workspace effect below
      return;
    }
    untrack(() => apply());
  });

  function reset() {
    fTemplate = fStatus = fTrigger = fUser = fHost = '';
    apply();
  }

  async function cancelRun(r: Run) {
    const ws = session.slug;
    if (!ws) return;
    rowBusy = r.id;
    error = null;
    try {
      const updated = await api.runs.cancel(ws, r.id);
      items = applyRunUpdate(items, updated, applied);
    } catch (e) {
      error = describeError(e);
    } finally {
      rowBusy = null;
    }
  }

  async function runAgain(r: Run) {
    const ws = session.slug;
    if (!ws) return;
    rowBusy = r.id;
    error = null;
    try {
      again = await prepareRunAgain(ws, r);
    } catch (e) {
      error = describeError(e);
    } finally {
      rowBusy = null;
    }
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
    untrack(() => {
      applied = formFilter();
      void load(ws, applied);
    });

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

<PageHeader title="Runs" description="History and live output of playbook runs." />

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
      <label class={label} for="f-user">User</label>
      <select id="f-user" class={input} bind:value={fUser}>
        <option value="">All</option>
        {#each userChoices as u (u.id)}<option value={u.id}>{u.label}</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="f-host">Host</label>
      <input
        id="f-host"
        class={input}
        bind:value={fHost}
        oninput={hostInput}
        placeholder="exact name"
      />
    </div>
    <button type="button" class={btn} onclick={reset}>Reset</button>
  </form>

  <FormError id="runs-error" message={error} />

  {#if loading && items.length === 0}
    <ListSkeleton />
  {:else if items.length === 0}
    <div class="empty">No runs found.</div>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Status</th><th class={th}>Template</th><th class={th}>Trigger</th>
            <th class={th}>Hosts</th><th class={th}>Created</th><th class={th}>Duration</th>
            {#if canCancel || canRun}<th class={th}></th>{/if}
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as r (r.id)}
            <tr>
              <td class={td}><a href="/runs/{r.id}"><StatusBadge status={r.status} /></a></td>
              <td class={td}>
                <a
                  class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
                  href="/runs/{r.id}">{r.template_name}</a
                >
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
                  <span class="text-xs text-bad"
                    >({r.hosts_failed + r.hosts_unreachable} failed)</span
                  >
                {/if}
              </td>
              <td class={td}>{fmtDate(r.created_at)}</td>
              <td class={td}>{formatDuration(r.started_at, r.finished_at, now)}</td>
              {#if canCancel || canRun}
                <td class="{td} whitespace-nowrap">
                  <div class="flex gap-2">
                    {#if canCancelRun(r, canCancel)}
                      <button
                        class={btnDanger}
                        disabled={rowBusy === r.id}
                        onclick={() => cancelRun(r)}>Cancel</button
                      >
                    {/if}
                    {#if canRun && r.template_id}
                      <button class={btn} disabled={rowBusy === r.id} onclick={() => runAgain(r)}
                        >Run again</button
                      >
                    {/if}
                  </div>
                </td>
              {/if}
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

{#if again && session.slug}
  <RunDialog
    ws={session.slug}
    template={again.template}
    prefill={again.prefill}
    notCarried={again.notCarried}
    onclose={() => (again = null)}
  />
{/if}
