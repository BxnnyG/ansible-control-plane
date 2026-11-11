<script lang="ts">
  import { api, type SecretMeta, type SecretType } from '$lib/api';
  import { SECRET_TYPE_LABEL, compatibleSecrets, secretValue } from '$lib/connectors';
  import type { ConnectorType } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btn, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    ctype,
    secretId = $bindable(null),
    canCreate,
    id = 'conn-secret',
    title = '',
  }: {
    ws: string;
    ctype: ConnectorType;
    /** Selected secret id; null means no credential. */
    secretId: string | null;
    canCreate: boolean;
    /** Element id of the select; unique when several pickers share a page. */
    id?: string;
    /** Label text; defaults to the connector wording ("Credential"). */
    title?: string;
  } = $props();

  let secrets = $state<SecretMeta[]>([]);
  let creating = $state(false);
  let newType = $state<SecretType>('api_token');
  let newName = $state('');
  let partA = $state('');
  let partB = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);

  const options = $derived(compatibleSecrets(ctype, secrets));

  async function load() {
    try {
      secrets = await api.secrets.list(ws);
    } catch (e) {
      error = describeError(e);
    }
  }

  $effect(() => {
    void load();
  });

  $effect(() => {
    // Keep the new-secret type valid for the selected connector type.
    if (!ctype.secret_types.includes(newType)) newType = ctype.secret_types[0] ?? 'api_token';
  });

  async function create() {
    busy = true;
    error = null;
    try {
      const s = await api.secrets.create(ws, {
        name: newName,
        type: newType,
        value: secretValue(newType, partA, partB),
      });
      partA = '';
      partB = '';
      newName = '';
      creating = false;
      await load();
      secretId = s.id;
    } catch (e) {
      error = describeError(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="space-y-2">
  <label class={label} for={id}>
    {title || `Credential${ctype.secret_required ? '' : ' (optional)'}`}
  </label>
  <select {id} class={input} bind:value={secretId} required={ctype.secret_required}>
    <option value={null}>None</option>
    {#each options as s (s.id)}
      <option value={s.id}>{s.name} ({SECRET_TYPE_LABEL[s.type] ?? s.type})</option>
    {/each}
  </select>
  <p class="text-xs text-muted">
    Accepted: {ctype.secret_types.map((t) => SECRET_TYPE_LABEL[t] ?? t).join(', ')}. The value is
    never shown again.
    {#if ctype.generates_secret}
      Leave on None to generate a secret; it is shown once after creating the connector.
    {/if}
  </p>
  {#if canCreate && !creating}
    <button type="button" class={btn} onclick={() => (creating = true)}>New secret</button>
  {/if}
  {#if creating}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <!-- Enter in a field must not submit a surrounding form. -->
    <fieldset
      class="space-y-2 rounded-xl border border-border p-3"
      onkeydown={(e) => {
        if (e.key === 'Enter' && e.target instanceof HTMLInputElement) e.preventDefault();
      }}
    >
      <legend class="px-1 text-sm">New secret</legend>
      {#if ctype.secret_types.length > 1}
        <select aria-label="Secret type" class={input} bind:value={newType}>
          {#each ctype.secret_types as t (t)}
            <option value={t}>{SECRET_TYPE_LABEL[t] ?? t}</option>
          {/each}
        </select>
      {/if}
      <input aria-label="Secret name" class={input} placeholder="Name" bind:value={newName} />
      {#if newType === 'api_key_pair'}
        <input
          aria-label="API key"
          class={input}
          placeholder="API key"
          autocomplete="off"
          bind:value={partA}
        />
        <input
          aria-label="API secret"
          type="password"
          class={input}
          placeholder="API secret"
          autocomplete="new-password"
          bind:value={partB}
        />
      {:else if newType === 'ssh_key'}
        <textarea
          aria-label="Secret value"
          class="{input} font-mono"
          rows="6"
          placeholder="Private key"
          autocomplete="off"
          spellcheck="false"
          bind:value={partA}
        ></textarea>
      {:else}
        <input
          aria-label="Secret value"
          type="password"
          class={input}
          placeholder="Value"
          autocomplete="new-password"
          bind:value={partA}
        />
      {/if}
      <FormError id="secret-create-error" message={error} />
      <div class="flex gap-2">
        <button
          type="button"
          class={btn}
          disabled={busy || !newName || !partA || (newType === 'api_key_pair' && !partB)}
          onclick={create}>Create secret</button
        >
        <button type="button" class={btn} onclick={() => (creating = false)}>Cancel</button>
      </div>
    </fieldset>
  {:else}
    <FormError id="secret-load-error" message={error} />
  {/if}
</div>
