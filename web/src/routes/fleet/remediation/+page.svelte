<script lang="ts">
  import { api, type RemediationRule, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { describeTarget } from '$lib/remediation';
  import { severityLabel } from '$lib/fleet';
  import { session } from '$lib/session.svelte';
  import { btn, btnDanger, btnPrimary, card, td, th } from '$lib/ui';
  import FleetTabs from '$lib/components/fleet/FleetTabs.svelte';
  import RemediationRuleForm from '$lib/components/fleet/RemediationRuleForm.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let rules = $state<RemediationRule[]>([]);
  let templates = $state<Template[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let editing = $state<RemediationRule | 'new' | null>(null);
  let deleting = $state<RemediationRule | null>(null);

  const canManage = $derived(session.can('fleet.manage'));

  async function load(ws: string) {
    loading = true;
    error = null;
    try {
      [rules, templates] = await Promise.all([
        api.remediation.list(ws),
        api.templates.list(ws).catch(() => [] as Template[]),
      ]);
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (session.slug && canManage) void load(session.slug);
    else loading = false;
  });

  async function confirmDelete() {
    if (!deleting || !session.slug) return;
    try {
      await api.remediation.remove(session.slug, deleting.id);
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

<svelte:head><title>Remediation - Ansible Control Plane</title></svelte:head>

<header class="mb-4 flex items-start justify-between gap-4">
  <div>
    <h1 class="text-2xl font-semibold">Remediation</h1>
    <p class="mt-1 text-muted">
      Map alerts to a template and a limit. Rules only offer a Fix button on matching alerts;
      nothing runs automatically.
    </p>
  </div>
  {#if canManage && editing === null}
    <button class={btnPrimary} onclick={() => (editing = 'new')}>New rule</button>
  {/if}
</header>
<FleetTabs />
<FormError id="rem-error" message={error} />

{#if !canManage}
  <p class="text-muted">
    Managing remediation rules needs the fleet.manage permission on the whole workspace.
  </p>
{:else}
  {#if editing !== null && session.slug}
    <div class="{card} mb-6 max-w-xl">
      {#key editing === 'new' ? 'new' : editing.id}
        <RemediationRuleForm
          ws={session.slug}
          rule={editing === 'new' ? null : editing}
          {templates}
          ondone={done}
          oncancel={() => (editing = null)}
        />
      {/key}
    </div>
  {/if}

  {#if deleting}
    <div role="alertdialog" aria-label="Confirm delete" class="{card} mb-6 max-w-xl">
      <p>Delete rule <strong>{deleting.name}</strong>? Runs already started are not affected.</p>
      <div class="mt-3 flex gap-2">
        <button class={btnDanger} onclick={confirmDelete}>Delete</button>
        <button class={btn} onclick={() => (deleting = null)}>Cancel</button>
      </div>
    </div>
  {/if}

  {#if loading}
    <p class="text-muted">Loading...</p>
  {:else if rules.length === 0}
    <div class="empty">No remediation rules yet.</div>
  {:else}
    <div class="tbl-wrap">
      <table class="w-full">
        <thead class="bg-surface-2">
          <tr>
            <th class={th}>Name</th><th class={th}>Matches</th><th class={th}>Template</th>
            <th class={th}>Limit</th><th class={th}>Check first</th><th class={th}>Enabled</th>
            <th class={th}><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each rules as r (r.id)}
            <tr>
              <td class={td}>{r.name}</td>
              <td class="{td} text-xs">
                {r.connector_type ?? 'any connector'}, from {severityLabel(r.min_severity)}
                {#if r.service_regex}<div class="font-mono">service: {r.service_regex}</div>{/if}
                {#if r.summary_regex}<div class="font-mono">summary: {r.summary_regex}</div>{/if}
              </td>
              <td class={td}>{r.template}</td>
              <td class={td}>{describeTarget(r)}</td>
              <td class={td}>{r.check_first ? 'Yes' : 'No'}</td>
              <td class={td}>{r.enabled ? 'Yes' : 'No'}</td>
              <td class="{td} text-right whitespace-nowrap">
                <button class={btn} onclick={() => (editing = r)}>Edit</button>
                <button class={btnDanger} onclick={() => (deleting = r)}>Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
