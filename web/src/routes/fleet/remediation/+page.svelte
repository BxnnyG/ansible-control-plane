<script lang="ts">
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { toasts } from '$lib/toasts.svelte';
  import ListSkeleton from '$lib/components/ListSkeleton.svelte';
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
      toasts.success('Remediation rule deleted.');
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

<svelte:head><title>Remediation - Hosts - Ansible Control Plane</title></svelte:head>

<PageHeader
  title="Remediation"
  description="Map alerts to a template and a limit. Rules only offer a Fix button on matching alerts; nothing runs automatically."
>
  {#snippet actions()}
    {#if canManage && editing === null}
      <button class={btnPrimary} onclick={() => (editing = 'new')}>New rule</button>
    {/if}
  {/snippet}
</PageHeader>
<FleetTabs />
<FormError id="rem-error" message={error} onretry={() => session.slug && load(session.slug)} />

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
    <ConfirmDialog
      title="Confirm delete"
      onconfirm={confirmDelete}
      oncancel={() => (deleting = null)}
    >
      <p>Delete rule <strong>{deleting.name}</strong>? Runs already started are not affected.</p>
    </ConfirmDialog>
  {/if}

  {#if loading}
    <ListSkeleton />
  {:else if rules.length === 0}
    <EmptyState
      title="No remediation rules yet."
      hint="A remediation rule maps an alert to a template so the alert gets a Fix button."
    >
      {#if canManage}
        <button class={btnPrimary} onclick={() => (editing = 'new')}>New rule</button>
      {/if}
    </EmptyState>
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
