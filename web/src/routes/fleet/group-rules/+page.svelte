<script lang="ts">
  import { api, type GroupRule, type GroupRulePreview, type Inventory } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { describeMatch, describeRuleError, summarizePlan } from '$lib/groupRules';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, btnPrimary, card, fmtDate, td, th } from '$lib/ui';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import GroupRuleForm from '$lib/components/fleet/GroupRuleForm.svelte';
  import PlanView from '$lib/components/fleet/PlanView.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let rules = $state<GroupRule[]>([]);
  let inventories = $state<Inventory[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let notice = $state<string | null>(null);
  let editing = $state<GroupRule | 'new' | null>(null);
  let deleting = $state<GroupRule | null>(null);
  let preview = $state<GroupRulePreview | null>(null);
  let confirmApply = $state(false);
  let busy = $state(false);

  const canRead = $derived(session.can('fleet.read'));
  const canManage = $derived(session.can('fleet.manage'));
  const canApply = $derived(canManage && session.can('inventory.write'));

  function invName(id: string): string {
    return inventories.find((i) => i.id === id)?.name ?? id;
  }

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      [rules, inventories] = await Promise.all([
        api.groupRules.list(ws),
        api.inventories.list(ws).catch(() => [] as Inventory[]),
      ]);
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

  function fail(e: unknown) {
    error = describeRuleError(e) ?? describeError(e);
  }

  async function doPreview(r: GroupRule) {
    if (!session.slug) return;
    busy = true;
    error = null;
    notice = null;
    confirmApply = false;
    try {
      preview = await api.groupRules.preview(session.slug, r.id);
    } catch (e) {
      preview = null;
      fail(e);
    } finally {
      busy = false;
    }
  }

  async function doApply() {
    if (!session.slug || !preview) return;
    busy = true;
    error = null;
    try {
      const res = await api.groupRules.apply(session.slug, preview.rule.id);
      const r = res.result;
      notice = `Applied "${res.rule.name}": ${(r.added ?? []).length} added, ${(r.removed ?? []).length} removed, ${(r.hosts_created ?? []).length} host(s) created${r.commit ? ' (committed to Git)' : ''}.`;
      preview = null;
      confirmApply = false;
      await load(session.slug);
    } catch (e) {
      confirmApply = false;
      fail(e);
    } finally {
      busy = false;
    }
  }

  async function confirmDelete() {
    if (!deleting || !session.slug) return;
    try {
      await api.groupRules.remove(session.slug, deleting.id);
      if (preview?.rule.id === deleting.id) preview = null;
      deleting = null;
      await load(session.slug);
    } catch (e) {
      error = describeError(e);
      deleting = null;
    }
  }

  function done() {
    editing = null;
    if (session.slug) void load(session.slug);
  }
</script>

<svelte:head><title>Group rules - Ansible Control Plane</title></svelte:head>

<header class="mb-4 flex items-start justify-between gap-4">
  <div>
    <h1 class="text-2xl font-semibold">Group rules</h1>
    <p class="mt-1 text-muted">Fill inventory groups automatically from fleet data.</p>
  </div>
  {#if canManage && editing === null}
    <button class={btnPrimary} onclick={() => (editing = 'new')}>New rule</button>
  {/if}
</header>
<FleetTabs />
<FormError id="rules-error" message={error} />
{#if notice}<p role="status" class="mb-3 text-sm">{notice}</p>{/if}

{#if !canRead}
  <p class="text-muted">You do not have permission to view the fleet in this workspace.</p>
{:else}
  {#if editing !== null && session.slug}
    <div class="{card} mb-6 max-w-xl">
      {#key editing === 'new' ? 'new' : editing.id}
        <GroupRuleForm
          ws={session.slug}
          rule={editing === 'new' ? null : editing}
          {inventories}
          ondone={done}
          oncancel={() => (editing = null)}
        />
      {/key}
    </div>
  {/if}

  {#if deleting}
    <div role="alertdialog" aria-label="Confirm delete" class="{card} mb-6 max-w-xl">
      <p>
        Delete rule <strong>{deleting.name}</strong>? Existing group members stay in the inventory.
      </p>
      <div class="mt-3 flex gap-2">
        <button class={btnDanger} onclick={confirmDelete}>Delete</button>
        <button class={btn} onclick={() => (deleting = null)}>Cancel</button>
      </div>
    </div>
  {/if}

  {#if preview}
    <section aria-label="Preview" class="{card} mb-6 max-w-2xl">
      <h2 class="mb-2 font-medium">
        Preview: {preview.rule.name}
        <span class="text-xs font-normal text-muted">({preview.inventory_mode} inventory)</span>
      </h2>
      <PlanView plan={preview.plan} group={preview.rule.group} />
      <div class="mt-3 flex gap-2">
        {#if canApply && !confirmApply}
          <button
            class={btnPrimary}
            disabled={busy || !preview.rule.enabled || summarizePlan(preview.plan).empty}
            onclick={() => (confirmApply = true)}>Apply</button
          >
        {/if}
        <button class={btn} onclick={() => (preview = null)}>Close</button>
      </div>
      {#if confirmApply}
        <div
          role="alertdialog"
          aria-label="Confirm apply"
          class="mt-3 rounded-md border border-border p-3"
        >
          <p class="text-sm">
            Apply this change to inventory <strong>{invName(preview.rule.inventory_id)}</strong>?
            {#if preview.inventory_mode === 'git'}A commit is written to the inventory's write
              branch.{/if}
          </p>
          <div class="mt-2 flex gap-2">
            <button class={btnPrimary} disabled={busy} onclick={doApply}>Confirm apply</button>
            <button class={btn} onclick={() => (confirmApply = false)}>Cancel</button>
          </div>
        </div>
      {/if}
    </section>
  {/if}

  {#if loading}
    <p class="text-muted">Loading...</p>
  {:else if rules.length === 0}
    <div class="rounded-lg border border-dashed border-border p-8 text-center text-muted">
      No group rules yet.
    </div>
  {:else}
    <div class="overflow-x-auto rounded-lg border border-border">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Match</th><th class={th}>Target</th>
            <th class={th}>Mode</th><th class={th}>Enabled</th><th class={th}>Last applied</th>
            <th class={th}><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each rules as r (r.id)}
            <tr>
              <td class={td}>{r.name}</td>
              <td class={td}>{describeMatch(r)}</td>
              <td class={td}>{invName(r.inventory_id)} / {r.group}</td>
              <td class={td}>{r.mode}</td>
              <td class={td}>{r.enabled ? 'Yes' : 'No'}</td>
              <td class={td}>{fmtDate(r.last_applied_at)}</td>
              <td class="{td} text-right whitespace-nowrap">
                <button class={btn} disabled={busy} onclick={() => doPreview(r)}>Preview</button>
                {#if canManage}
                  <button class={btn} onclick={() => (editing = r)}>Edit</button>
                  <button class={btnDanger} onclick={() => (deleting = r)}>Delete</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
