<script lang="ts">
  import { api, type NotificationChannel, type SecretMeta } from '$lib/api';
  import { describeError } from '$lib/errors';
  import {
    CHANNEL_TYPES,
    buildChannelConfig,
    fieldsFromConfig,
    secretRequired,
    usableSecrets,
    validateChannel,
    type ChannelType,
  } from '$lib/notifications';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    channel = null,
    secrets,
    ondone,
    oncancel,
  }: {
    ws: string;
    /** Channel being edited; null creates a new one. */
    channel?: NotificationChannel | null;
    secrets: SecretMeta[];
    ondone: () => void;
    oncancel: () => void;
  } = $props();

  // Initial values are captured once; the parent re-mounts the form per channel.
  // svelte-ignore state_referenced_locally
  let type = $state<ChannelType>(channel?.type ?? 'webhook');
  // svelte-ignore state_referenced_locally
  let name = $state(channel?.name ?? '');
  // svelte-ignore state_referenced_locally
  let f = $state(fieldsFromConfig(channel?.type ?? 'webhook', channel?.config));
  // svelte-ignore state_referenced_locally
  let secretId = $state<string | null>(channel?.secret_id ?? null);
  // svelte-ignore state_referenced_locally
  let caPem = $state(channel?.tls_ca_pem ?? '');
  // svelte-ignore state_referenced_locally
  let skipVerify = $state(channel?.tls_skip_verify ?? false);
  // svelte-ignore state_referenced_locally
  let enabled = $state(channel?.enabled ?? true);
  let error = $state<string | null>(null);
  let busy = $state(false);

  const options = $derived(usableSecrets(secrets));
  const needsSecret = $derived(secretRequired(type, f));
  const usesTls = $derived(type !== 'smtp' || f.security !== 'none');

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = validateChannel(type, name, f, secretId !== null);
    if (error) return;
    busy = true;
    try {
      const common = {
        name: name.trim(),
        config: buildChannelConfig(type, f),
        secret_id: secretId,
        tls_ca_pem: caPem.trim(),
        tls_skip_verify: skipVerify,
        enabled,
      };
      if (channel) await api.notifications.channels.update(ws, channel.id, common);
      else await api.notifications.channels.create(ws, { type, ...common });
      ondone();
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="space-y-3" aria-label={channel ? 'Edit channel' : 'New channel'}>
  <div>
    <label class={label} for="nc-type">Type</label>
    <select id="nc-type" class={input} bind:value={type} disabled={!!channel}>
      {#each CHANNEL_TYPES as t (t.type)}<option value={t.type}>{t.label}</option>{/each}
    </select>
  </div>
  <div>
    <label class={label} for="nc-name">Name</label>
    <input id="nc-name" class={input} bind:value={name} required />
  </div>

  {#if type === 'webhook'}
    <div>
      <label class={label} for="nc-url">URL</label>
      <input id="nc-url" class={input} bind:value={f.url} placeholder="https://host/path" />
    </div>
    <div>
      <label class={label} for="nc-auth">Authentication</label>
      <select id="nc-auth" class={input} bind:value={f.auth}>
        <option value="hmac">HMAC signature (secret is the key)</option>
        <option value="bearer">Bearer token (secret is the token)</option>
      </select>
    </div>
  {:else if type === 'matrix'}
    <div>
      <label class={label} for="nc-hs">Homeserver URL</label>
      <input
        id="nc-hs"
        class={input}
        bind:value={f.homeserver}
        placeholder="https://matrix.example"
      />
    </div>
    <div>
      <label class={label} for="nc-room">Room ID</label>
      <input id="nc-room" class={input} bind:value={f.roomId} placeholder="!room:example" />
    </div>
  {:else if type === 'smtp'}
    <div>
      <label class={label} for="nc-host">Host</label>
      <input id="nc-host" class={input} bind:value={f.host} />
    </div>
    <div>
      <label class={label} for="nc-port">Port (optional)</label>
      <input id="nc-port" class={input} inputmode="numeric" bind:value={f.port} />
    </div>
    <div>
      <label class={label} for="nc-sec">Security</label>
      <select id="nc-sec" class={input} bind:value={f.security}>
        <option value="starttls">STARTTLS</option>
        <option value="tls">TLS</option>
        <option value="none">None (plaintext)</option>
      </select>
    </div>
    {#if f.security === 'none'}
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={f.insecure} /> I understand that mail is sent unencrypted
      </label>
    {/if}
    <div>
      <label class={label} for="nc-user">Username (optional)</label>
      <input id="nc-user" class={input} bind:value={f.username} autocomplete="off" />
    </div>
    <div>
      <label class={label} for="nc-from">From</label>
      <input id="nc-from" class={input} bind:value={f.from} placeholder="acp@example.org" />
    </div>
    <div>
      <label class={label} for="nc-to">To (comma separated)</label>
      <input id="nc-to" class={input} bind:value={f.to} placeholder="ops@example.org" />
    </div>
  {:else if type === 'gotify'}
    <div>
      <label class={label} for="nc-url">URL</label>
      <input id="nc-url" class={input} bind:value={f.url} placeholder="https://gotify.example" />
    </div>
    <div>
      <label class={label} for="nc-prio">Priority (optional)</label>
      <input id="nc-prio" class={input} inputmode="numeric" bind:value={f.priority} />
    </div>
  {:else if type === 'ntfy'}
    <div>
      <label class={label} for="nc-url">URL</label>
      <input id="nc-url" class={input} bind:value={f.url} placeholder="https://ntfy.example" />
    </div>
    <div>
      <label class={label} for="nc-topic">Topic</label>
      <input id="nc-topic" class={input} bind:value={f.topic} />
    </div>
    <div>
      <label class={label} for="nc-prio">Priority (optional, 1-5)</label>
      <input id="nc-prio" class={input} inputmode="numeric" bind:value={f.priority} />
    </div>
  {/if}

  <div>
    <label class={label} for="nc-secret">
      Credential{needsSecret ? '' : ' (optional)'}
    </label>
    <select id="nc-secret" class={input} bind:value={secretId}>
      <option value={null}>None</option>
      {#each options as s (s.id)}<option value={s.id}>{s.name} ({s.type})</option>{/each}
    </select>
    <p class="mt-1 text-xs text-muted">
      Choose an existing secret (token, API token or password); manage secrets on the Secrets page.
      The value is never shown.
    </p>
  </div>

  {#if usesTls}
    <div>
      <label class={label} for="nc-ca">Custom CA certificate, PEM (optional)</label>
      <textarea id="nc-ca" class="{input} font-mono text-xs" rows="3" bind:value={caPem}></textarea>
    </div>
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={skipVerify} /> Skip TLS verification (insecure)
    </label>
  {/if}
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={enabled} /> Enabled
  </label>

  <FormError id="nc-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{channel ? 'Save' : 'Create'}</button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
