<script lang="ts">
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { toasts } from '$lib/toasts.svelte';
  import Breadcrumbs from '$lib/components/Breadcrumbs.svelte';
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, type Run, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { buildRunQuery } from '$lib/runs';
  import { session } from '$lib/session.svelte';
  import { loadTemplateRefs, type TemplateRefs } from '$lib/templateData';
  import { btn, btnDanger, btnPrimary, card, fmtDate, td, th } from '$lib/ui';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import FormError from '$lib/components/FormError.svelte';
  import StatusBadge from '$lib/components/runs/StatusBadge.svelte';
  import RunDialog from '$lib/components/templates/RunDialog.svelte';
  import SchedulesPanel from '$lib/components/templates/SchedulesPanel.svelte';
  import TemplateForm from '$lib/components/templates/TemplateForm.svelte';
  import TemplateWebhookPanel from '$lib/components/templates/TemplateWebhookPanel.svelte';

  const id = $derived(page.params.id ?? '');
  const ws = $derived(session.slug);
  const canWrite = $derived(session.can('template.write'));
  const canRun = $derived(session.can('run.execute'));

  let template = $state<Template | null>(null);
  let refs = $state<TemplateRefs>({ projects: [], inventories: [], secrets: [], identities: [] });
  let recent = $state<Run[]>([]);
  let error = $state<string | null>(null);
  let loading = $state(true);
  let editing = $state(false);
  let confirmDelete = $state(false);
  let running = $state(false);

  async function load(slug: string, tid: string) {
    error = null;
    loading = true;
    const wantRuns = untrack(() => session.can('run.read'));
    try {
      const [t, r, rr] = await Promise.all([
        api.templates.get(slug, tid),
        loadTemplateRefs(slug),
        wantRuns ? api.runs.list(slug, buildRunQuery({ template: tid, limit: 5 })) : null,
      ]);
      template = t;
      refs = r;
      recent = rr?.items ?? [];
    } catch (e) {
      error = describeError(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    const slug = ws;
    const tid = id;
    if (slug && tid) untrack(() => void load(slug, tid));
  });

  async function remove() {
    if (!ws || !template) return;
    try {
      await api.templates.remove(ws, template.id);
      toasts.success('Template deleted.');
      await goto('/templates');
    } catch (e) {
      error = describeError(e);
      confirmDelete = false;
    }
  }

  const projectName = $derived(
    refs.projects.find((p) => p.id === template?.project_id)?.name ?? '-',
  );
  const inventoryName = $derived(
    refs.inventories.find((i) => i.id === template?.inventory_id)?.name ?? '-',
  );
</script>

<svelte:head><title>{template?.name ?? 'Template'} - Ansible Control Plane</title></svelte:head>

<Breadcrumbs trail={[{ label: 'Templates', href: '/templates' }]} />
{#if !template}
  <FormError id="template-page-error" message={error} />
  {#if loading}
    <div class="space-y-4" aria-busy="true" aria-label="Loading template">
      <Skeleton class="h-7 w-64" />
      <div class="{card} space-y-3">
        <Skeleton class="h-4 w-full" />
        <Skeleton class="h-4 w-3/4" />
        <Skeleton class="h-4 w-1/2" />
      </div>
      <div class="{card} space-y-3">
        <Skeleton class="h-4 w-full" />
        <Skeleton class="h-4 w-2/3" />
      </div>
    </div>
  {:else if error}
    <button class={btn} onclick={() => ws && load(ws, id)}>Retry</button>
  {/if}
{:else}
  <FormError id="template-page-error" message={error} />
{/if}

{#if template && ws}
  <header
    class="mb-5 flex flex-wrap items-end justify-between gap-x-4 gap-y-3 border-b border-border pb-4"
  >
    <div>
      <h1 class="text-xl font-semibold">{template.name}</h1>
      {#if template.description}<p class="mt-1 text-muted">{template.description}</p>{/if}
    </div>
    <div class="flex gap-2">
      {#if canRun}<button class={btnPrimary} onclick={() => (running = true)}>Run</button>{/if}
      {#if canWrite && !editing}
        <button class={btn} onclick={() => (editing = true)}>Edit</button>
        <button class={btnDanger} onclick={() => (confirmDelete = true)}>Delete</button>
      {/if}
    </div>
  </header>

  {#if confirmDelete}
    <ConfirmDialog
      title="Confirm delete"
      onconfirm={remove}
      oncancel={() => (confirmDelete = false)}
    >
      <p>
        Delete template <strong>{template.name}</strong>? Queued runs are cancelled; run history is
        kept.
      </p>
    </ConfirmDialog>
  {/if}

  {#if editing}
    <div class="{card} mb-6 max-w-4xl">
      {#key template.updated_at}
        <TemplateForm
          {ws}
          {template}
          {...refs}
          ondone={(t) => {
            template = t;
            editing = false;
          }}
          oncancel={() => (editing = false)}
        />
      {/key}
    </div>
  {:else}
    <section class="{card} mb-6" aria-label="Details">
      <dl class="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <div>
          <dt class="text-muted">Playbook</dt>
          <dd class="font-mono">{template.playbook}</dd>
        </div>
        <div>
          <dt class="text-muted">Project</dt>
          <dd>{projectName}</dd>
        </div>
        <div>
          <dt class="text-muted">Inventory</dt>
          <dd>{inventoryName}</dd>
        </div>
        <div>
          <dt class="text-muted">Default limit</dt>
          <dd>{template.default_limit || '-'}</dd>
        </div>
        <div>
          <dt class="text-muted">Tags / skip tags</dt>
          <dd>{template.default_tags || '-'} / {template.default_skip_tags || '-'}</dd>
        </div>
        <div>
          <dt class="text-muted">Check / diff / verbosity</dt>
          <dd>
            {template.check_mode ? 'check' : 'apply'} / {template.diff_mode ? 'diff' : 'no diff'} / {template.verbosity}
          </dd>
        </div>
        <div>
          <dt class="text-muted">Max. concurrent</dt>
          <dd>{template.max_concurrent}</dd>
        </div>
        <div>
          <dt class="text-muted">Updated</dt>
          <dd>{fmtDate(template.updated_at)}</dd>
        </div>
      </dl>
    </section>
  {/if}

  <div class="grid gap-6">
    {#if recent.length > 0}
      <section class={card} aria-labelledby="recent-h">
        <div class="mb-2 flex items-center justify-between">
          <h2 id="recent-h" class="text-sm font-semibold">Recent runs</h2>
          <a
            class="text-sm underline decoration-border-strong underline-offset-2 hover:decoration-current"
            href="/runs?template={template.id}">All runs</a
          >
        </div>
        <table class="w-full">
          <thead class="bg-surface-2"
            ><tr
              ><th class={th}>Status</th><th class={th}>Created</th><th class={th}>Trigger</th></tr
            ></thead
          >
          <tbody class="divide-y divide-border">
            {#each recent as r (r.id)}
              <tr>
                <td class={td}><a href="/runs/{r.id}"><StatusBadge status={r.status} /></a></td>
                <td class={td}>{fmtDate(r.created_at)}</td>
                <td class={td}>{r.trigger}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </section>
    {/if}
    <SchedulesPanel
      {ws}
      templateId={template.id}
      {template}
      canRead={session.can('schedule.read')}
      canWrite={session.can('schedule.write')}
    />
    {#if canWrite}<TemplateWebhookPanel {ws} templateId={template.id} />{/if}
  </div>

  {#if running}<RunDialog {ws} {template} onclose={() => (running = false)} />{/if}
{/if}
