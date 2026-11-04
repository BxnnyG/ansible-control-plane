<script lang="ts">
  import { api, type SecretMeta, type SecretType } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    secret = null,
    ondone,
    oncancel,
  }: {
    ws: string;
    /** Secret being edited; null creates a new one. */
    secret?: SecretMeta | null;
    ondone: () => void;
    oncancel: () => void;
  } = $props();

  const TYPES: { value: SecretType; label: string }[] = [
    { value: 'ssh_key', label: 'SSH private key' },
    { value: 'password', label: 'Password' },
    { value: 'token', label: 'API token' },
    { value: 'vault_password', label: 'Vault password' },
    { value: 'file', label: 'File' },
  ];

  // Initial values are captured once; the parent re-mounts the form per secret.
  // svelte-ignore state_referenced_locally
  let name = $state(secret?.name ?? '');
  // svelte-ignore state_referenced_locally
  let type = $state<SecretType>(secret?.type ?? 'ssh_key');
  // svelte-ignore state_referenced_locally
  let description = $state(secret?.description ?? '');
  let value = $state('');
  let error = $state<string | null>(null);
  let busy = $state(false);

  const fingerprint = $derived(
    typeof secret?.metadata?.fingerprint === 'string' ? secret.metadata.fingerprint : null,
  );

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = null;
    try {
      if (secret) {
        // Only send the value when the user typed a replacement.
        await api.secrets.update(ws, secret.id, {
          name,
          description,
          ...(value ? { value } : {}),
        });
      } else {
        await api.secrets.create(ws, { name, type, description, value });
      }
      value = '';
      ondone();
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="space-y-3" aria-label={secret ? 'Edit secret' : 'New secret'}>
  <div>
    <label class={label} for="sec-name">Name</label>
    <input id="sec-name" class={input} bind:value={name} required />
  </div>
  <div>
    <label class={label} for="sec-type">Type</label>
    <select id="sec-type" class={input} bind:value={type} disabled={!!secret}>
      {#each TYPES as t (t.value)}<option value={t.value}>{t.label}</option>{/each}
    </select>
  </div>
  <div>
    <label class={label} for="sec-desc">Description</label>
    <input id="sec-desc" class={input} bind:value={description} />
  </div>
  {#if fingerprint}
    <p class="text-sm text-muted">
      Current fingerprint: <code class="break-all">{fingerprint}</code>
    </p>
  {/if}
  <div>
    <label class={label} for="sec-value">
      {secret ? 'New value (leave empty to keep the stored one)' : 'Value'}
    </label>
    {#if type === 'ssh_key' || type === 'file'}
      <textarea
        id="sec-value"
        class="{input} font-mono"
        rows="6"
        bind:value
        required={!secret}
        autocomplete="off"
        spellcheck="false"
      ></textarea>
    {:else}
      <input
        id="sec-value"
        type="password"
        class={input}
        bind:value
        required={!secret}
        autocomplete="new-password"
      />
    {/if}
    <p class="mt-1 text-xs text-muted">Write-only: the value is never shown again.</p>
  </div>
  <FormError id="sec-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{secret ? 'Save' : 'Create'}</button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
