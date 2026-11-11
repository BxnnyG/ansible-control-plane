<script lang="ts">
  import { pollUntil } from '$lib/polling';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { toasts } from '$lib/toasts.svelte';
  import { untrack } from 'svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
  import { api, type Connector, type ConnectorCreated, type ConnectorType } from '$lib/api';
  import { fmtDuration, statusClass, statusLabel } from '$lib/connectors';
  import { warningsOf } from '$lib/dashboard';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, btnPrimary, card, fmtDate, td, th } from '$lib/ui';
  import ConnectorForm from '$lib/components/connectors/ConnectorForm.svelte';
  import WarningsBadge from '$lib/components/connectors/WarningsBadge.svelte';
  import RowActions from '$lib/components/RowActions.svelte';
  import SyncHistory from '$lib/components/connectors/SyncHistory.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let items = $state<Connector[]>([]);
  let types = $state<ConnectorType[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let editing = $state<Connector | 'new' | null>(null);
  let deleting = $state<Connector | null>(null);
  let history = $state<string | null>(null);
  let busyId = $state<string | null>(null);

  const canRead = $derived(session.can('connector.read'));
  const canWrite = $derived(session.can('connector.write'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      [items, types] = await Promise.all([
        api.connectors.list(ws, (d) => {
          if (session.slug === ws) items = d;
        }),
        api.connectorTypes(ws),
      ]);
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    const ws = session.slug;
    if (ws && canRead) untrack(() => void load(ws));
    else loading = false;
  });

  function typeLabel(t: string): string {
    return types.find((x) => x.type === t)?.label ?? t;
  }

  async function test(c: Connector) {
    if (!session.slug) return;
    busyId = c.id;
    error = null;
    try {
      const r = await api.connectors.test(session.slug, c.id);
      if (r.ok) toasts.success(`${c.name}: connection OK.`);
      else toasts.error(`${c.name}: connection failed: ${r.error}`);
    } catch (e) {
      error = describeError(e);
    } finally {
      busyId = null;
    }
  }

  let syncing = $state<Record<string, boolean>>({});
  const anySyncing = $derived(Object.values(syncing).some(Boolean));

  /** Requests a sync and polls until the connector reports a new sync time (max. 60 s). */
  async function syncOne(c: Connector): Promise<'ok' | 'error' | 'pending' | 'failed'> {
    const ws = session.slug;
    if (!ws) return 'failed';
    syncing[c.id] = true;
    try {
      await api.connectors.sync(ws, c.id);
      const before = c.last_sync_at;
      const r = await pollUntil(
        async () => {
          const list = await api.connectors.list(ws);
          if (session.slug === ws) items = list;
          return list.find((x) => x.id === c.id);
        },
        (cur) => !cur || cur.last_sync_at !== before,
        { intervalMs: 2000, maxMs: 60_000 },
      );
      if (!r.met) return 'pending';
      return r.value?.status === 'error' ? 'error' : 'ok';
    } catch (e) {
      error = describeError(e);
      return 'failed';
    } finally {
      syncing[c.id] = false;
    }
  }

  async function sync(c: Connector) {
    error = null;
    toasts.info(`${c.name}: sync started.`);
    const r = await syncOne(c);
    if (r === 'ok') toasts.success(`${c.name}: sync finished.`);
    else if (r === 'error') toasts.error(`${c.name}: sync finished with an error. See History.`);
    else if (r === 'pending') toasts.info(`${c.name}: still syncing. Check History in a moment.`);
  }

  async function syncAll() {
    error = null;
    const targets = items.filter((c) => c.enabled && !syncing[c.id]);
    if (targets.length === 0) return;
    toasts.info(`Sync started for ${targets.length} connector(s).`);
    const results = await Promise.all(targets.map(syncOne));
    const failed = results.filter((r) => r === 'error' || r === 'failed').length;
    const pending = results.filter((r) => r === 'pending').length;
    if (failed > 0) toasts.error(`${failed} of ${targets.length} sync(s) failed. See History.`);
    else if (pending > 0) toasts.info(`${pending} sync(s) still running.`);
    else toasts.success(`All ${targets.length} sync(s) finished.`);
  }

  async function confirmDelete() {
    if (!deleting || !session.slug) return;
    try {
      await api.connectors.remove(session.slug, deleting.id);
      toasts.success('Connector deleted.');
      deleting = null;
      await load(session.slug);
    } catch (e) {
      error = describeError(e);
      deleting = null;
    }
  }

  // Shown once after creating a connector that generated a webhook secret.
  let created = $state<ConnectorCreated | null>(null);

  function done(saved: ConnectorCreated) {
    created = saved.generated_secret ? saved : null;
    editing = null;
    if (session.slug) void load(session.slug);
  }
</script>

<svelte:head><title>Connectors - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Connectors"
  description="Read-only links to OPNsense, Proxmox and other systems that feed the fleet view."
>
  {#snippet actions()}
    {#if canWrite && items.some((c) => c.enabled)}
      <button class={btn} disabled={anySyncing} onclick={syncAll}>Sync all</button>
    {/if}
    {#if canWrite && editing === null && types.length > 0}
      <button class={btnPrimary} onclick={() => (editing = 'new')}>New connector</button>
    {/if}
  {/snippet}
</PageHeader>

<FormError
  id="connectors-error"
  message={error}
  onretry={() => session.slug && load(session.slug)}
/>
{#if created}
  <div role="status" class="{card} mb-6 max-w-2xl">
    <p>
      Connector <strong>{created.name}</strong> created. Copy the webhook secret now: it is shown only
      once.
    </p>
    <dl class="mt-2 space-y-1 text-sm">
      <dt class="text-muted">Endpoint (POST)</dt>
      <dd class="font-mono break-all">{created.webhook_url || created.webhook_path}</dd>
      <dt class="text-muted">Secret (Authorization: Bearer ...)</dt>
      <dd class="font-mono break-all">{created.generated_secret}</dd>
    </dl>
    <button class="{btn} mt-3" onclick={() => (created = null)}>I have copied it</button>
  </div>
{/if}

{#if !canRead}
  <p class="text-muted">You do not have permission to view connectors in this workspace.</p>
{:else}
  {#if editing !== null && session.slug}
    <div class="{card} mb-6 max-w-xl">
      {#key editing === 'new' ? 'new' : editing.id}
        <ConnectorForm
          ws={session.slug}
          {types}
          connector={editing === 'new' ? null : editing}
          ondone={done}
          oncancel={() => (editing = null)}
        />
      {/key}
    </div>
  {/if}

  {#if deleting}
    <ConfirmDialog
      title="Confirm delete"
      onconfirm={confirmDelete}
      oncancel={() => (deleting = null)}
    >
      <p>
        Delete connector <strong>{deleting.name}</strong>? Its observations are removed from the
        fleet view.
      </p>
    </ConfirmDialog>
  {/if}

  {#if loading}
    <ListSkeleton />
  {:else if items.length === 0}
    <EmptyState
      title="No connectors yet."
      hint="Connectors read hosts and alerts from OPNsense, Proxmox and other systems."
    >
      {#if canWrite && types.length > 0}
        <button class={btnPrimary} onclick={() => (editing = 'new')}>New connector</button>
      {/if}
    </EmptyState>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Type</th><th class={th}>Status</th>
            <th class={th}>Last success</th><th class={th}>Last error</th>
            <th class={th}>Counts</th><th class={th}><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each items as c (c.id)}
            <tr>
              <td class={td}>
                {c.name}
                {#if !c.enabled}<span class="text-xs text-muted">(disabled)</span>{/if}
                {#if c.webhook_path}
                  <div class="font-mono text-xs text-muted">POST {c.webhook_path}</div>
                {/if}
                <WarningsBadge warnings={warningsOf(c)} />
              </td>
              <td class={td}>{typeLabel(c.type)}</td>
              <td class={td}>
                <span class={statusClass(c.status)}>
                  {statusLabel(c.status)}
                </span>
              </td>
              <td class={td}>
                {fmtDate(c.last_success_at)}
                <div class="text-xs text-muted">Took {fmtDuration(c.last_duration_ms)}</div>
              </td>
              <td class="{td} max-w-xs text-xs break-words text-bad">
                {c.last_error || '-'}
              </td>
              <td class="{td} whitespace-nowrap">
                {c.counts?.networks ?? 0} networks
                <div class="text-xs text-muted">
                  {c.counts?.observations ?? 0} obs. · {c.counts?.alerts ?? 0} alerts
                </div>
              </td>
              <td class="{td} text-right">
                <RowActions label="Actions for {c.name}">
                  <button class={btn} onclick={() => (history = history === c.id ? null : c.id)}>
                    History
                  </button>
                  {#if canWrite}
                    <button class={btn} disabled={busyId === c.id} onclick={() => test(c)}
                      >Test</button
                    >
                    <button
                      class={btn}
                      disabled={busyId === c.id || syncing[c.id] || !c.enabled}
                      onclick={() => sync(c)}
                    >
                      {syncing[c.id] ? 'Syncing...' : 'Sync now'}
                    </button>
                    <button class={btn} onclick={() => (editing = c)}>Edit</button>
                    <button class={btnDanger} onclick={() => (deleting = c)}>Delete</button>
                  {/if}
                </RowActions>
              </td>
            </tr>
            {#if history === c.id && session.slug}
              <tr>
                <td colspan="7" class="bg-surface-2 p-3">
                  <SyncHistory ws={session.slug} id={c.id} />
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
