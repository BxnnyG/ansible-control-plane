<script lang="ts">
  import { api, type SecretMeta, type VaultIdentity, type VaultProvider } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    identity = null,
    secrets,
    ondone,
    oncancel,
  }: {
    ws: string;
    /** Identity being edited; null creates a new one. */
    identity?: VaultIdentity | null;
    secrets: SecretMeta[];
    ondone: () => void;
    oncancel: () => void;
  } = $props();

  // Initial values are captured once; the parent re-mounts the form per identity.
  // svelte-ignore state_referenced_locally
  let name = $state(identity?.label ?? '');
  // svelte-ignore state_referenced_locally
  let description = $state(identity?.description ?? '');
  // svelte-ignore state_referenced_locally
  let provider = $state<VaultProvider>(identity?.provider ?? 'secret');
  // svelte-ignore state_referenced_locally
  let secretId = $state(identity?.secret_id ?? '');
  // svelte-ignore state_referenced_locally
  let scriptPath = $state(identity?.script_path ?? '');
  // svelte-ignore state_referenced_locally
  let scriptArgs = $state((identity?.script_args ?? []).join('\n'));
  let error = $state<string | null>(null);
  let busy = $state(false);

  const vaultSecrets = $derived(secrets.filter((s) => s.type === 'vault_password'));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = null;
    const body = {
      label: name.trim(),
      description,
      provider,
      secret_id: provider === 'secret' ? secretId || null : null,
      script_path: provider === 'script' ? scriptPath.trim() : '',
      script_args:
        provider === 'script'
          ? scriptArgs
              .split('\n')
              .map((a) => a.trim())
              .filter((a) => a !== '')
          : [],
    };
    try {
      if (identity) await api.vaultIdentities.update(ws, identity.id, body);
      else await api.vaultIdentities.create(ws, body);
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
  aria-label={identity ? 'Edit vault identity' : 'New vault identity'}
>
  <div>
    <label class={label} for="vi-label">Label</label>
    <input id="vi-label" class={input} bind:value={name} required placeholder="default" />
    <p class="mt-1 text-xs text-muted">
      Matches the vault id in encrypted files (<code>--vault-id label@...</code>).
    </p>
  </div>
  <div>
    <label class={label} for="vi-desc">Description</label>
    <input id="vi-desc" class={input} bind:value={description} />
  </div>
  <div>
    <label class={label} for="vi-provider">Password source</label>
    <select id="vi-provider" class={input} bind:value={provider}>
      <option value="secret">Stored secret (vault password)</option>
      <option value="script">Script (executed by the worker)</option>
    </select>
  </div>
  {#if provider === 'secret'}
    <div>
      <label class={label} for="vi-secret">Secret</label>
      <select id="vi-secret" class={input} bind:value={secretId} required>
        <option value="">Select a secret...</option>
        {#each vaultSecrets as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
      </select>
      {#if vaultSecrets.length === 0}
        <p class="mt-1 text-xs text-muted">
          No secrets of type "vault_password" yet. Create one on the
          <a class="text-accent hover:underline" href="/secrets">Secrets</a> page.
        </p>
      {/if}
    </div>
  {:else}
    <div>
      <label class={label} for="vi-script">Script path</label>
      <input
        id="vi-script"
        class="{input} font-mono"
        bind:value={scriptPath}
        required
        placeholder="scripts/vault-pass.sh"
        spellcheck="false"
      />
      <p class="mt-1 text-xs text-muted">
        Relative to the project checkout. The app never runs it; the password is only available to
        runs, not to the in-app decrypt/encrypt actions.
      </p>
    </div>
    <div>
      <label class={label} for="vi-args">Arguments (one per line)</label>
      <textarea
        id="vi-args"
        class="{input} font-mono"
        rows="3"
        bind:value={scriptArgs}
        spellcheck="false"
      ></textarea>
    </div>
  {/if}
  <FormError id="vi-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{identity ? 'Save' : 'Create'}</button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
