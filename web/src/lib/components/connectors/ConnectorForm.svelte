<script lang="ts">
  import {
    api,
    type Connector,
    type ConnectorCreated,
    type ConnectorTestResult,
    type ConnectorType,
  } from '$lib/api';
  import { buildConfig, initialValues, invalidFields, needsSecret } from '$lib/connectors';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';
  import SecretPicker from './SecretPicker.svelte';

  let {
    ws,
    types,
    connector = null,
    ondone,
    oncancel,
    submitLabel,
  }: {
    ws: string;
    types: ConnectorType[];
    /** Connector being edited; null creates a new one. */
    connector?: Connector | null;
    ondone: (saved: ConnectorCreated) => void;
    oncancel: () => void;
    submitLabel?: string;
  } = $props();

  // Initial values are captured once; the parent re-mounts the form per connector.
  // svelte-ignore state_referenced_locally
  let typeKey = $state(connector?.type ?? types[0]?.type ?? '');
  const ctype = $derived(types.find((t) => t.type === typeKey));
  // svelte-ignore state_referenced_locally
  let name = $state(connector?.name ?? '');
  // svelte-ignore state_referenced_locally
  let baseUrl = $state(connector?.base_url ?? '');
  // svelte-ignore state_referenced_locally
  let interval = $state(String(connector?.interval_seconds ?? 300));
  // svelte-ignore state_referenced_locally
  let enabled = $state(connector?.enabled ?? true);
  // svelte-ignore state_referenced_locally
  let secretId = $state<string | null>(connector?.secret?.id ?? null);
  // svelte-ignore state_referenced_locally
  let values = $state<Record<string, string>>(ctype ? initialValues(ctype, connector) : {});
  let error = $state<string | null>(null);
  let busy = $state(false);
  let testing = $state(false);
  let testResult = $state<ConnectorTestResult | null>(null);

  function onTypeChange() {
    if (!ctype) return;
    values = initialValues(ctype, null);
    secretId = null;
    testResult = null;
  }

  function problems(): string | null {
    if (!ctype) return 'Choose a connector type.';
    if (!name.trim()) return 'Name is required.';
    const bad = invalidFields(ctype, values);
    if (bad.length) return `Check: ${bad.join(', ')}.`;
    if (ctype.base_url && !baseUrl.trim()) return 'Base URL is required.';
    if (ctype.secret_required && !secretId) return 'A credential is required.';
    return null;
  }

  async function test() {
    if (!ctype) return;
    testing = true;
    testResult = null;
    error = null;
    try {
      testResult = await api.connectors.testConfig(ws, {
        type: ctype.type,
        base_url: baseUrl.trim() || undefined,
        config: buildConfig(ctype, values),
        secret_id: secretId,
      });
    } catch (e) {
      error = describeError(e);
    } finally {
      testing = false;
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = problems();
    if (error || !ctype) return;
    busy = true;
    try {
      const common = {
        name: name.trim(),
        base_url: baseUrl.trim(),
        config: buildConfig(ctype, values),
        secret_id: secretId,
        interval_seconds: Number(interval) || 300,
        enabled,
      };
      const saved = connector
        ? await api.connectors.update(ws, connector.id, common)
        : await api.connectors.create(ws, { type: ctype.type, ...common });
      ondone(saved);
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
  aria-label={connector ? 'Edit connector' : 'New connector'}
>
  <div>
    <label class={label} for="conn-type">Type</label>
    <select
      id="conn-type"
      class={input}
      bind:value={typeKey}
      onchange={onTypeChange}
      disabled={!!connector}
    >
      {#each types as t (t.type)}<option value={t.type}>{t.label}</option>{/each}
    </select>
    {#if ctype?.help}<p class="mt-1 text-xs whitespace-pre-line text-muted">{ctype.help}</p>{/if}
  </div>
  <div>
    <label class={label} for="conn-name">Name</label>
    <input id="conn-name" class={input} bind:value={name} required />
  </div>
  {#if ctype}
    {#if ctype.base_url}
      <div>
        <label class={label} for="conn-url">Base URL</label>
        <input
          id="conn-url"
          class={input}
          bind:value={baseUrl}
          placeholder="https://host:port"
          required
        />
      </div>
    {/if}
    {#each ctype.fields as f (f.key)}
      <div>
        {#if f.kind === 'bool'}
          <label class="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={values[f.key] === 'true'}
              onchange={(e) => (values[f.key] = e.currentTarget.checked ? 'true' : 'false')}
            />
            {f.label}
          </label>
        {:else}
          <label class={label} for="conn-f-{f.key}">
            {f.label}{f.required ? '' : ' (optional)'}
          </label>
          <input
            id="conn-f-{f.key}"
            class={input}
            inputmode={f.kind === 'int' ? 'numeric' : undefined}
            bind:value={values[f.key]}
            required={f.required}
          />
        {/if}
        {#if f.help}<p class="mt-1 text-xs text-muted">{f.help}</p>{/if}
      </div>
    {/each}
    {#if needsSecret(ctype)}
      {#key ctype.type}
        <SecretPicker {ws} {ctype} bind:secretId canCreate={session.can('secrets.write')} />
      {/key}
    {/if}
  {/if}
  <div>
    <label class={label} for="conn-interval">Sync interval (seconds)</label>
    <input id="conn-interval" class={input} inputmode="numeric" bind:value={interval} />
  </div>
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={enabled} /> Enabled
  </label>

  {#if testResult}
    <p
      role="status"
      class="text-sm {testResult.ok
        ? 'text-green-700 dark:text-green-400'
        : 'text-red-600 dark:text-red-400'}"
    >
      {testResult.ok ? 'Connection OK.' : `Connection failed: ${testResult.error}`}
    </p>
  {/if}
  <FormError id="conn-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}
      >{submitLabel ?? (connector ? 'Save' : 'Create')}</button
    >
    <button type="button" class={btn} disabled={testing || !ctype} onclick={test}>
      {testing ? 'Testing...' : 'Test connection'}
    </button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
