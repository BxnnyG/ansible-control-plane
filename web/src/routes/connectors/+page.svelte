<script lang="ts">
  import { api, type Connector, type ConnectorCreated, type ConnectorType } from '$lib/api';
  import { fmtDuration, statusClass, statusLabel } from '$lib/connectors';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, btnPrimary, card, fmtDate, td, th } from '$lib/ui';
  import ConnectorForm from '$lib/components/connectors/ConnectorForm.svelte';
  import SyncHistory from '$lib/components/connectors/SyncHistory.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let items = $state<Connector[]>([]);
  let types = $state<ConnectorType[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let notice = $state<string | null>(null);
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
      [items, types] = await Promise.all([api.connectors.list(ws), api.connectorTypes(ws)]);
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (session.slug && canRead) void load(session.slug);
    else loading = false;
  });

  function typeLabel(t: string): string {
    return types.find((x) => x.type === t)?.label ?? t;
  }

  async function test(c: Connector) {
    if (!session.slug) return;
    busyId = c.id;
    notice = null;
    error = null;
    try {
      const r = await api.connectors.test(session.slug, c.id);
      notice = r.ok ? `${c.name}: connection OK.` : `${c.name}: connection failed: ${r.error}`;
    } catch (e) {
      error = describeError(e);
    } finally {
      busyId = null;
    }
  }

  async function sync(c: Connector) {
    if (!session.slug) return;
    busyId = c.id;
    notice = null;
    error = null;
    try {
      await api.connectors.sync(session.slug, c.id);
      notice = `${c.name}: sync requested. Refresh in a moment to see the result.`;
      await load(session.slug);
    } catch (e) {
      error = describeError(e);
    } finally {
      busyId = null;
    }
  }

  async function confirmDelete() {
    if (!deleting || !session.slug) return;
    try {
      await api.connectors.remove(session.slug, deleting.id);
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

<header class="mb-6 flex items-start justify-between gap-4">
  <div>
    <h1 class="text-2xl font-semibold">Connectors</h1>
    <p class="mt-1 text-muted">
      Read-only links to OPNsense, Proxmox and other systems that feed the fleet view.
    </p>
  </div>
  {#if canWrite && editing === null && types.length > 0}
    <button class={btnPrimary} onclick={() => (editing = 'new')}>New connector</button>
  {/if}
</header>

<FormError id="connectors-error" message={error} />
{#if notice}<p role="status" class="mb-3 text-sm">{notice}</p>{/if}
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
    <div role="alertdialog" aria-label="Confirm delete" class="{card} mb-6 max-w-xl">
      <p>
        Delete connector <strong>{deleting.name}</strong>? Its observations are removed from the
        fleet view.
      </p>
      <div class="mt-3 flex gap-2">
        <button class={btnDanger} onclick={confirmDelete}>Delete</button>
        <button class={btn} onclick={() => (deleting = null)}>Cancel</button>
      </div>
    </div>
  {/if}

  {#if loading}
    <p class="text-muted">Loading...</p>
  {:else if items.length === 0}
    <div class="rounded-lg border border-dashed border-border p-8 text-center text-muted">
      No connectors yet.
    </div>
  {:else}
    <div class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Type</th><th class={th}>Status</th>
            <th class={th}>Last success</th><th class={th}>Last error</th>
            <th class={th}>Duration</th><th class={th}>Networks</th><th class={th}>Obs.</th>
            <th class={th}>Alerts</th><th class={th}><span class="sr-only">Actions</span></th>
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
                {#each c.warnings ?? [] as w (w)}
                  <div class="text-xs text-amber-700 dark:text-amber-400">Warning: {w}</div>
                {/each}
                {#each c.counts?.warnings ?? [] as w (w)}
                  <div class="text-xs text-amber-700 dark:text-amber-400">Warning: {w}</div>
                {/each}
              </td>
              <td class={td}>{typeLabel(c.type)}</td>
              <td class={td}>
                <span class="rounded px-2 py-0.5 text-xs {statusClass(c.status)}">
                  {statusLabel(c.status)}
                </span>
              </td>
              <td class={td}>{fmtDate(c.last_success_at)}</td>
              <td class="{td} max-w-xs text-xs break-words text-red-600 dark:text-red-400">
                {c.last_error || '-'}
              </td>
              <td class={td}>{fmtDuration(c.last_duration_ms)}</td>
              <td class={td}>{c.counts?.networks ?? 0}</td>
              <td class={td}>{c.counts?.observations ?? 0}</td>
              <td class={td}>{c.counts?.alerts ?? 0}</td>
              <td class="{td} text-right whitespace-nowrap">
                <button class={btn} onclick={() => (history = history === c.id ? null : c.id)}>
                  History
                </button>
                {#if canWrite}
                  <button class={btn} disabled={busyId === c.id} onclick={() => test(c)}
                    >Test</button
                  >
                  <button
                    class={btn}
                    disabled={busyId === c.id || !c.enabled}
                    onclick={() => sync(c)}
                  >
                    Sync now
                  </button>
                  <button class={btn} onclick={() => (editing = c)}>Edit</button>
                  <button class={btnDanger} onclick={() => (deleting = c)}>Delete</button>
                {/if}
              </td>
            </tr>
            {#if history === c.id && session.slug}
              <tr>
                <td colspan="10" class="bg-surface-2 p-3">
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
