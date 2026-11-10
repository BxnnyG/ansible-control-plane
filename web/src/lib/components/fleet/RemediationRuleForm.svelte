<script lang="ts">
  import {
    api,
    type AlertSeverity,
    type FleetAlert,
    type RemediationRule,
    type RemediationTarget,
    type Template,
  } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { TARGETS } from '$lib/remediation';
  import { alertHost, severityClass, severityLabel } from '$lib/fleet';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    rule = null,
    templates,
    ondone,
    oncancel,
  }: {
    ws: string;
    rule?: RemediationRule | null;
    templates: Template[];
    ondone: () => void;
    oncancel: () => void;
  } = $props();

  const SEVERITIES: AlertSeverity[] = ['info', 'unknown', 'warning', 'critical'];

  // Initial values are captured once; the parent re-mounts the form per rule.
  // svelte-ignore state_referenced_locally
  let name = $state(rule?.name ?? '');
  // svelte-ignore state_referenced_locally
  let enabled = $state(rule?.enabled ?? true);
  // svelte-ignore state_referenced_locally
  let connectorType = $state(rule?.connector_type ?? '');
  // svelte-ignore state_referenced_locally
  let serviceRegex = $state(rule?.service_regex ?? '');
  // svelte-ignore state_referenced_locally
  let summaryRegex = $state(rule?.summary_regex ?? '');
  // svelte-ignore state_referenced_locally
  let minSeverity = $state<AlertSeverity>(rule?.min_severity ?? 'warning');
  // svelte-ignore state_referenced_locally
  let templateId = $state(rule?.template_id ?? templates[0]?.id ?? '');
  // svelte-ignore state_referenced_locally
  let target = $state<RemediationTarget>(rule?.target ?? 'host');
  // svelte-ignore state_referenced_locally
  let group = $state(rule?.group ?? '');
  // svelte-ignore state_referenced_locally
  let checkFirst = $state(rule?.check_first ?? false);
  let error = $state<string | null>(null);
  let busy = $state(false);
  let matches = $state<FleetAlert[] | null>(null);

  const template = $derived(templates.find((t) => t.id === templateId));

  async function test() {
    busy = true;
    error = null;
    try {
      matches = await api.remediation.preview(ws, {
        connector_type: connectorType.trim() || null,
        service_regex: serviceRegex,
        summary_regex: summaryRegex,
        min_severity: minSeverity,
      });
    } catch (e) {
      matches = null;
      error = describeError(e);
    } finally {
      busy = false;
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = null;
    try {
      const body = {
        name: name.trim(),
        enabled,
        connector_type: connectorType.trim() || null,
        service_regex: serviceRegex,
        summary_regex: summaryRegex,
        min_severity: minSeverity,
        template_id: templateId,
        target,
        group: target === 'group' ? group.trim() : '',
        check_first: checkFirst,
      };
      if (rule) await api.remediation.update(ws, rule.id, body);
      else await api.remediation.create(ws, body);
      ondone();
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form
  onsubmit={submit}
  class="space-y-3"
  aria-label={rule ? 'Edit remediation rule' : 'New remediation rule'}
>
  <div>
    <label class={label} for="rr-name">Name</label>
    <input id="rr-name" class={input} bind:value={name} required />
  </div>
  <fieldset class="space-y-3 rounded-xl border border-border p-3">
    <legend class="px-1 text-sm font-medium">Applies to open alerts that match</legend>
    <div>
      <label class={label} for="rr-conn">Connector type (empty: any)</label>
      <input id="rr-conn" class={input} bind:value={connectorType} placeholder="checkmk" />
    </div>
    <div>
      <label class={label} for="rr-svc">Service regex</label>
      <input
        id="rr-svc"
        class="{input} font-mono"
        bind:value={serviceRegex}
        placeholder="^Service nginx"
      />
    </div>
    <div>
      <label class={label} for="rr-sum">Summary regex</label>
      <input id="rr-sum" class="{input} font-mono" bind:value={summaryRegex} />
      <p class="mt-1 text-xs text-muted">
        RE2 syntax, not anchored. Empty matches everything. Prefix (?i) for case-insensitive.
      </p>
    </div>
    <div>
      <label class={label} for="rr-sev">Minimum severity</label>
      <select id="rr-sev" class={input} bind:value={minSeverity}>
        {#each SEVERITIES as s (s)}<option value={s}>{severityLabel(s)}</option>{/each}
      </select>
    </div>
    <button type="button" class={btn} disabled={busy} onclick={test}
      >Test against open alerts</button
    >
    {#if matches}
      <div role="status" class="text-sm">
        <p>{matches.length} open alert{matches.length === 1 ? '' : 's'} match.</p>
        <ul class="mt-1 max-h-40 space-y-1 overflow-y-auto">
          {#each matches.slice(0, 20) as a (a.id)}
            <li>
              <span class="rounded px-1.5 py-0.5 text-xs {severityClass(a.severity)}"
                >{severityLabel(a.severity)}</span
              >
              {alertHost(a)} - {a.service || a.summary || '-'}
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  </fieldset>
  <div>
    <label class={label} for="rr-tpl">Template</label>
    <select id="rr-tpl" class={input} bind:value={templateId} required>
      {#each templates as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
    </select>
    {#if template && !template.allow_limit_override}
      <p class="mt-1 text-xs text-bad">
        This template does not allow limit overrides; enable that first.
      </p>
    {/if}
  </div>
  <div>
    <label class={label} for="rr-target">Limit</label>
    <select id="rr-target" class={input} bind:value={target}>
      {#each TARGETS as t (t.value)}<option value={t.value}>{t.label}</option>{/each}
    </select>
  </div>
  {#if target === 'group'}
    <div>
      <label class={label} for="rr-group">Inventory group</label>
      <input id="rr-group" class={input} bind:value={group} required />
    </div>
  {/if}
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={checkFirst} /> Run in check mode first (you start the real run
    afterwards)
  </label>
  {#if checkFirst && template && !template.allow_check_override}
    <p class="text-xs text-bad">This template does not allow check mode overrides.</p>
  {/if}
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={enabled} /> Enabled
  </label>
  <FormError id="rr-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy || !templateId}>{rule ? 'Save' : 'Create'}</button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
